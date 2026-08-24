package kiroprovider

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/contrib/qoder-cli-provider/internal/provider"
)

type TextHandler func(string) error

type promptResult struct {
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
	} `json:"usage"`
}

func Run(ctx context.Context, account Account, invocation provider.Invocation, onText TextHandler) (provider.Result, error) {
	if onText == nil {
		onText = func(string) error { return nil }
	}
	workspace, err := os.MkdirTemp("", "cliproxy-kiro-*")
	if err != nil {
		return provider.Result{}, err
	}
	defer os.RemoveAll(workspace)

	callback, err := newToolCallback(invocation.Tools)
	if err != nil {
		return provider.Result{}, err
	}
	defer callback.Close()
	callback.TracePath = filepath.Join(workspace, "mcp-trace.log")
	if err = configureWorkspace(workspace, account, invocation.Tools.Specs, callback); err != nil {
		return provider.Result{}, err
	}

	agentName := "cli-proxy-text"
	if len(invocation.Tools.Specs) > 0 {
		// Kiro CLI 2.x currently exposes project MCP tools reliably only through
		// its built-in agent. ACP permissions below still reject every built-in
		// tool and allow only the caller's openai_tools entries.
		agentName = "kiro_default"
	}
	args := []string{"acp", "--model", invocation.Model, "--agent", agentName, "--agent-engine", "v2"}
	cmd := exec.CommandContext(ctx, account.CLIPath, args...)
	cmd.Dir = workspace
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return provider.Result{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return provider.Result{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return provider.Result{}, err
	}
	if err = cmd.Start(); err != nil {
		return provider.Result{}, fmt.Errorf("start Kiro CLI: %w", err)
	}

	var stderrText strings.Builder
	var stderrMu sync.Mutex
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		buffer := make([]byte, 4096)
		for {
			count, readErr := stderr.Read(buffer)
			if count > 0 {
				stderrMu.Lock()
				stderrText.Write(buffer[:count])
				stderrMu.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}()

	client := newACPClient(stdout, stdin, invocation.Tools)
	var initialize map[string]any
	if err = client.call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "CLIProxyAPI", "version": "0.1.0"},
	}, &initialize); err != nil {
		return stopWithError(cmd, stderrDone, &stderrMu, &stderrText, err)
	}

	servers, err := mcpServers(account, invocation.Tools.Specs, callback)
	if err != nil {
		return stopWithError(cmd, stderrDone, &stderrMu, &stderrText, err)
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err = client.call(ctx, "session/new", map[string]any{"cwd": workspace, "mcpServers": servers}, &session); err != nil {
		return stopWithError(cmd, stderrDone, &stderrMu, &stderrText, err)
	}
	if session.SessionID == "" {
		return stopWithError(cmd, stderrDone, &stderrMu, &stderrText, fmt.Errorf("Kiro ACP returned an empty session id"))
	}
	if len(invocation.Tools.Specs) > 0 {
		select {
		case <-callback.Ready:
		case <-ctx.Done():
			return stopWithError(cmd, stderrDone, &stderrMu, &stderrText, ctx.Err())
		case <-time.After(30 * time.Second):
			return stopWithError(cmd, stderrDone, &stderrMu, &stderrText, fmt.Errorf("Kiro MCP tool catalog did not become ready within 30 seconds"))
		}
	}
	promptText := invocation.SystemPrompt + "\n\n" + invocation.Prompt
	promptDone := make(chan error, 1)
	var outcome promptResult
	startPrompt := func(text string) {
		outcome = promptResult{}
		go func() {
			promptDone <- client.call(ctx, "session/prompt", map[string]any{
				"sessionId": session.SessionID,
				"prompt":    []map[string]any{{"type": "text", "text": text}},
			}, &outcome)
		}()
	}
	startPrompt(promptText)

	result := provider.Result{Model: invocation.Model, FinishReason: "stop"}
	var text strings.Builder
	var diagnostics strings.Builder
	toolPromptRetries := 0
	for {
		select {
		case <-ctx.Done():
			return stopWithError(cmd, stderrDone, &stderrMu, &stderrText, ctx.Err())
		case call := <-callback.Calls:
			result.ToolCalls = []provider.ToolCall{call}
			result.FinishReason = "tool_calls"
			result.Text = text.String()
			result.Usage = estimatedUsage(invocation, result)
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			<-stderrDone
			return result, nil
		case rawUpdate := <-client.updates:
			delta := messageDelta(rawUpdate)
			if delta != "" {
				text.WriteString(delta)
				if !invocation.Tools.Required {
					if err = onText(delta); err != nil {
						return stopWithError(cmd, stderrDone, &stderrMu, &stderrText, err)
					}
				}
			} else if len(invocation.Tools.Specs) > 0 && diagnostics.Len() < 8000 {
				diagnostics.Write(rawUpdate)
				diagnostics.WriteByte('\n')
			}
		case err = <-promptDone:
			select {
			case call := <-callback.Calls:
				result.ToolCalls = []provider.ToolCall{call}
				result.FinishReason = "tool_calls"
			default:
			}
			if err != nil {
				return stopWithError(cmd, stderrDone, &stderrMu, &stderrText, err)
			}
			if invocation.Tools.Required && len(result.ToolCalls) == 0 && toolPromptRetries == 0 {
				toolPromptRetries++
				text.Reset()
				diagnostics.Reset()
				startPrompt("The openai_tools MCP server is now initialized. Call the required caller-supplied tool for the preceding request now. Do not use built-in tools and do not answer in text.")
				continue
			}
			result.Text = text.String()
			result.Usage = provider.Usage{
				PromptTokens: outcome.Usage.InputTokens, CompletionTokens: outcome.Usage.OutputTokens,
				TotalTokens: outcome.Usage.InputTokens + outcome.Usage.OutputTokens,
			}
			if result.Usage.TotalTokens == 0 {
				result.Usage = estimatedUsage(invocation, result)
			}
			if invocation.Tools.Required && len(result.ToolCalls) == 0 {
				detail := strings.TrimSpace(result.Text)
				diagnostic := strings.TrimSpace(diagnostics.String())
				if trace, traceErr := os.ReadFile(callback.TracePath); traceErr == nil && len(trace) > 0 {
					diagnostic += "\nMCP trace:\n" + string(trace)
				}
				if detail != "" {
					if diagnostic != "" {
						return stopWithError(cmd, stderrDone, &stderrMu, &stderrText, fmt.Errorf("Kiro did not return the required external tool call; response: %s; ACP updates: %s", detail, diagnostic))
					}
					return stopWithError(cmd, stderrDone, &stderrMu, &stderrText, fmt.Errorf("Kiro did not return the required external tool call; response: %s", detail))
				}
				return stopWithError(cmd, stderrDone, &stderrMu, &stderrText, fmt.Errorf("Kiro did not return the required external tool call"))
			}
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			<-stderrDone
			return result, nil
		}
	}
}

func configureWorkspace(root string, account Account, specs []provider.ToolSpec, callback *callbackServer) error {
	agentsDir := filepath.Join(root, ".kiro", "agents")
	settingsDir := filepath.Join(root, ".kiro", "settings")
	err := os.MkdirAll(agentsDir, 0o700)
	if err == nil {
		err = os.MkdirAll(settingsDir, 0o700)
	}
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(settingsDir, "cli.json"), []byte(`{"chat.disableInheritingDefaultResources":true}`), 0o600); err != nil {
		return err
	}
	if err = writeAgent(filepath.Join(agentsDir, "cli-proxy-text.json"), false); err == nil && len(specs) > 0 {
		err = writeAgent(filepath.Join(agentsDir, "cli-proxy-tools.json"), true)
	}
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		return nil
	}
	server, err := mcpServer(account, specs, callback)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{"openai_tools": server},
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(settingsDir, "mcp.json"), raw, 0o600)
}

func writeAgent(path string, withTools bool) error {
	name := "cli-proxy-text"
	prompt := "You are the model backend of an external agent harness. The external harness owns conversation state and executes every tool. Never inspect files, run commands, modify the workspace, delegate work, or use capabilities not explicitly supplied by the upstream harness. Return only the next assistant response."
	tools := []string{}
	if withTools {
		name = "cli-proxy-tools"
		prompt = "You are the model backend of an external agent harness. Only use tools supplied by the openai_tools MCP server. Never use built-in capabilities. When a supplied tool is needed, call it natively and stop because the upstream harness executes it. Return only the next assistant response."
		tools = []string{"@openai_tools"}
	}
	raw, _ := json.MarshalIndent(map[string]any{
		"name": name, "description": "CLIProxyAPI token-only model backend", "prompt": prompt,
		"tools": tools, "allowedTools": tools, "resources": []string{}, "mcpServers": map[string]any{}, "includeMcpJson": withTools,
	}, "", "  ")
	return os.WriteFile(path, raw, 0o600)
}

type callbackServer struct {
	URL       string
	ReadyURL  string
	Secret    string
	Calls     chan provider.ToolCall
	Ready     chan struct{}
	server    *http.Server
	plan      provider.ToolPlan
	TracePath string
	readyOnce sync.Once
}

func newToolCallback(plan provider.ToolPlan) (*callbackServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	secretBytes := make([]byte, 24)
	_, _ = rand.Read(secretBytes)
	baseURL := "http://" + listener.Addr().String()
	callback := &callbackServer{
		URL: baseURL + "/tool", ReadyURL: baseURL + "/ready", Secret: hex.EncodeToString(secretBytes),
		Calls: make(chan provider.ToolCall, 4), Ready: make(chan struct{}), plan: plan,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/tool", callback.handle)
	mux.HandleFunc("/ready", callback.handleReady)
	callback.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = callback.server.Serve(listener) }()
	return callback, nil
}

func (c *callbackServer) handleReady(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.Header.Get("X-CLI-Proxy-Tool-Secret") != c.Secret {
		http.Error(response, "not found", http.StatusNotFound)
		return
	}
	c.readyOnce.Do(func() { close(c.Ready) })
	response.WriteHeader(http.StatusNoContent)
}

func (c *callbackServer) handle(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.Header.Get("X-CLI-Proxy-Tool-Secret") != c.Secret {
		http.Error(response, "not found", http.StatusNotFound)
		return
	}
	var body struct {
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	}
	if json.NewDecoder(request.Body).Decode(&body) != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	call, err := provider.NormalizeExternalToolCall("", body.Name, body.Input, c.plan)
	if err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	select {
	case c.Calls <- call:
		response.WriteHeader(http.StatusAccepted)
	default:
		http.Error(response, "tool callback queue is full", http.StatusConflict)
	}
}

func (c *callbackServer) Close() {
	if c != nil && c.server != nil {
		_ = c.server.Close()
	}
}

func mcpServers(account Account, specs []provider.ToolSpec, callback *callbackServer) ([]map[string]any, error) {
	if len(specs) == 0 {
		return []map[string]any{}, nil
	}
	server, err := mcpServer(account, specs, callback)
	if err != nil {
		return nil, err
	}
	environment, _ := server["env"].(map[string]string)
	envList := make([]map[string]string, 0, len(environment))
	for name, value := range environment {
		envList = append(envList, map[string]string{"name": name, "value": value})
	}
	return []map[string]any{{
		"name": "openai_tools", "command": server["command"], "args": server["args"], "env": envList,
	}}, nil
}

func mcpServer(account Account, specs []provider.ToolSpec, callback *callbackServer) (map[string]any, error) {
	raw, err := json.Marshal(specs)
	if err != nil {
		return nil, err
	}
	environment := map[string]string{
		"CLI_PROXY_EXTERNAL_TOOLS":       base64.RawURLEncoding.EncodeToString(raw),
		"CLI_PROXY_TOOL_CALLBACK_URL":    callback.URL,
		"CLI_PROXY_MCP_READY_URL":        callback.ReadyURL,
		"CLI_PROXY_TOOL_CALLBACK_SECRET": callback.Secret,
		"CLI_PROXY_MCP_TRACE_FILE":       callback.TracePath,
	}
	return map[string]any{
		"command": account.BridgePath, "args": []string{}, "env": environment,
		"disabled": false, "autoApprove": []string{"*"},
	}, nil
}

func messageDelta(raw json.RawMessage) string {
	var params struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &params) != nil || params.Update.SessionUpdate != "agent_message_chunk" || params.Update.Content.Type != "text" {
		return ""
	}
	return params.Update.Content.Text
}

func estimatedUsage(invocation provider.Invocation, result provider.Result) provider.Usage {
	prompt := int64((len(invocation.SystemPrompt) + len(invocation.Prompt) + 3) / 4)
	completion := int64((len(result.Text) + 3) / 4)
	for _, call := range result.ToolCalls {
		completion += int64((len(call.Name) + len(call.Arguments) + 3) / 4)
	}
	return provider.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion}
}

func stopWithError(cmd *exec.Cmd, stderrDone <-chan struct{}, mu *sync.Mutex, stderr *strings.Builder, err error) (provider.Result, error) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	<-stderrDone
	mu.Lock()
	detail := strings.TrimSpace(stderr.String())
	mu.Unlock()
	if detail != "" {
		if len(detail) > 2000 {
			detail = detail[len(detail)-2000:]
		}
		return provider.Result{}, fmt.Errorf("%w: %s", err, detail)
	}
	return provider.Result{}, err
}
