package provider

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const qoderStreamIdleTimeout = 4 * time.Minute

type TextHandler func(string) error

func Run(ctx context.Context, account Account, invocation Invocation, onText TextHandler) (Result, error) {
	if onText == nil {
		onText = func(string) error { return nil }
	}
	callback, errCallback := newToolCallback(invocation.Tools)
	if errCallback != nil {
		return Result{}, errCallback
	}
	defer callback.Close()
	args, cleanup, errArgs := commandArgs(account, invocation, callback)
	if errArgs != nil {
		return Result{}, errArgs
	}
	defer cleanup()

	cmd := exec.CommandContext(ctx, account.CLIPath, args...)
	configureProcessGroup(cmd)
	cmd.Env = cleanEnvironment(account)
	if account.CWD != "" {
		cmd.Dir = account.CWD
	} else {
		cmd.Dir = os.TempDir()
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, fmt.Errorf("open qodercli stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("open qodercli stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, fmt.Errorf("open qodercli stderr: %w", err)
	}
	if err = cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start qodercli: %w", err)
	}

	var stderrBuffer strings.Builder
	var stderrMu sync.Mutex
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			stderrMu.Lock()
			if stderrBuffer.Len() > 32*1024 {
				current := stderrBuffer.String()
				stderrBuffer.Reset()
				stderrBuffer.WriteString(current[len(current)/2:])
			}
			stderrBuffer.WriteString(scanner.Text())
			stderrBuffer.WriteByte('\n')
			stderrMu.Unlock()
		}
	}()

	encoder := json.NewEncoder(stdin)
	if err = encoder.Encode(initializeRequest(invocation.SystemPrompt)); err != nil {
		_ = killProcessTree(cmd)
		return Result{}, fmt.Errorf("initialize qodercli: %w", err)
	}

	events := make(chan qoderEvent)
	readErrors := make(chan error, 1)
	go readEvents(stdout, events, readErrors)
	if err = waitForInitialize(ctx, events, readErrors); err != nil {
		_ = killProcessTree(cmd)
		_ = cmd.Wait()
		<-stderrDone
		return Result{}, withStderr(err, stderrText(&stderrMu, &stderrBuffer))
	}
	if len(invocation.Tools.Specs) > 0 {
		select {
		case <-callback.Ready:
		case <-ctx.Done():
			_ = killProcessTree(cmd)
			_ = cmd.Wait()
			<-stderrDone
			return Result{}, ctx.Err()
		case <-time.After(30 * time.Second):
			_ = killProcessTree(cmd)
			_ = cmd.Wait()
			<-stderrDone
			return Result{}, withStderr(fmt.Errorf("qodercli MCP tool catalog did not become ready within 30 seconds"), stderrText(&stderrMu, &stderrBuffer))
		}
	}
	if err = encoder.Encode(userRequest(invocation.Prompt)); err != nil {
		_ = killProcessTree(cmd)
		return Result{}, fmt.Errorf("send qodercli request: %w", err)
	}

	result, runErr := consumeEvents(ctx, events, readErrors, callback.Calls, invocation, onText, func(event qoderEvent) error {
		return allowGenericToolControlRequest(encoder, event)
	})
	_ = stdin.Close()
	if runErr != nil || len(result.ToolCalls) > 0 {
		_ = killProcessTree(cmd)
	}
	waitErr := cmd.Wait()
	<-stderrDone
	if runErr != nil {
		return Result{}, withStderr(runErr, stderrText(&stderrMu, &stderrBuffer))
	}
	if waitErr != nil && len(result.ToolCalls) == 0 {
		return Result{}, withStderr(fmt.Errorf("qodercli exited: %w", waitErr), stderrText(&stderrMu, &stderrBuffer))
	}
	return result, nil
}

func commandArgs(account Account, invocation Invocation, callback *toolCallbackServer) ([]string, func(), error) {
	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--include-partial-messages",
		"--model", invocation.Model,
		"--tools", "",
		"--setting-sources", "",
		"--disable-builtin-skills",
		"--system-prompt", invocation.SystemPrompt,
	}
	cleanupPaths := make([]string, 0, len(invocation.Attachments)+1)
	cleanup := func() {
		for index := len(cleanupPaths) - 1; index >= 0; index-- {
			_ = os.Remove(cleanupPaths[index])
		}
	}
	attachmentDir := ""
	if len(invocation.Attachments) > 0 {
		var err error
		attachmentDir, err = os.MkdirTemp("", "qoder-attachments-*")
		if err != nil {
			return nil, func() {}, fmt.Errorf("create qoder attachment directory: %w", err)
		}
		cleanupPaths = append(cleanupPaths, attachmentDir)
	}
	for _, attachment := range invocation.Attachments {
		extension, ok := imageExtension(attachment.MediaType)
		if !ok {
			cleanup()
			return nil, func() {}, fmt.Errorf("unsupported attachment media type %q", attachment.MediaType)
		}
		fileName := strings.TrimSuffix(filepath.Base(attachment.FileName), filepath.Ext(attachment.FileName)) + extension
		path := filepath.Join(attachmentDir, fileName)
		cleanupPaths = append(cleanupPaths, path)
		if err := os.WriteFile(path, attachment.Data, 0o600); err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("write qoder attachment: %w", err)
		}
		args = append(args, "--attachment", path)
	}
	if invocation.MaxTokens > 0 {
		args = append(args, "--max-output-tokens", fmt.Sprint(invocation.MaxTokens))
	}
	if account.ConfigDir != "" {
		args = append(args, "--config-dir", account.ConfigDir)
	}
	if len(invocation.Tools.Specs) == 0 {
		return args, cleanup, nil
	}
	if account.BridgePath == "" {
		cleanup()
		return nil, func() {}, fmt.Errorf("qoder auth %q requires bridge_path when tools are supplied", account.ID)
	}
	configFile, err := writeMCPConfig(account.BridgePath, invocation.Tools.Specs, callback)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	cleanupPaths = append(cleanupPaths, configFile)
	args = append(args,
		"--mcp-config", configFile,
		"--strict-mcp-config",
		"--allowed-mcp-server-names", "openai_tools",
		"--permission-mode", "bypass_permissions",
	)
	for _, spec := range invocation.Tools.Specs {
		args = append(args, "--allowed-tools", "mcp__openai_tools__"+spec.SDKName)
	}
	return args, cleanup, nil
}

func writeMCPConfig(bridgePath string, tools []ToolSpec, callback *toolCallbackServer) (string, error) {
	rawTools, err := json.Marshal(tools)
	if err != nil {
		return "", fmt.Errorf("encode MCP tools: %w", err)
	}
	environment := map[string]string{
		"CLI_PROXY_EXTERNAL_TOOLS": base64.RawURLEncoding.EncodeToString(rawTools),
	}
	if callback != nil {
		environment["CLI_PROXY_TOOL_CALLBACK_URL"] = callback.URL
		environment["CLI_PROXY_MCP_READY_URL"] = callback.ReadyURL
		environment["CLI_PROXY_TOOL_CALLBACK_SECRET"] = callback.Secret
	}
	config := map[string]any{"mcpServers": map[string]any{
		"openai_tools": map[string]any{
			"command": bridgePath,
			"args":    []string{},
			"env":     environment,
		},
	}}
	rawConfig, _ := json.Marshal(config)
	file, err := os.CreateTemp("", "qoder-mcp-*.json")
	if err != nil {
		return "", fmt.Errorf("create MCP config: %w", err)
	}
	name := file.Name()
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(rawConfig)
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("write MCP config: %w", err)
	}
	return name, nil
}

func cleanEnvironment(account Account) []string {
	blocked := map[string]struct{}{
		"QODER_AGENT_SDK_AUTH_PAYLOAD_FILE": {},
		"QODER_SDK_AUTH_PAYLOAD_FILE":       {},
		"QODER_AGENT_SDK_ENTRYPOINT":        {},
		"QODER_AGENT_SDK_VERSION":           {},
		"QODER_AGENT_SDK_LANGUAGE":          {},
	}
	environment := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if _, skip := blocked[key]; !skip {
			environment = append(environment, item)
		}
	}
	return environment
}

func initializeRequest(systemPrompt string) map[string]any {
	return map[string]any{
		"type":       "control_request",
		"request_id": completionID("init"),
		"request": map[string]any{
			"type":                           "initialize",
			"modelPolicyProvider":            false,
			"supportsCatalogReadyInitialize": true,
			"initializeTimeoutMs":            120000,
			"systemPrompt":                   systemPrompt,
		},
	}
}

func userRequest(prompt string) map[string]any {
	return map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": prompt}},
		},
		"parent_tool_use_id": nil,
	}
}

func readEvents(reader io.Reader, events chan<- qoderEvent, readErrors chan<- error) {
	defer close(events)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event qoderEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			readErrors <- fmt.Errorf("decode qodercli event: %w", err)
			return
		}
		events <- event
	}
	if err := scanner.Err(); err != nil {
		readErrors <- fmt.Errorf("read qodercli event: %w", err)
		return
	}
	readErrors <- io.EOF
}

func waitForInitialize(ctx context.Context, events <-chan qoderEvent, readErrors <-chan error) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErrors:
			return err
		case event, ok := <-events:
			if !ok {
				return io.ErrUnexpectedEOF
			}
			if event.Type == "control_response" {
				if detail := resultError(event); detail != "" {
					return fmt.Errorf("qodercli initialization failed: %s", detail)
				}
				return nil
			}
			// The personal Qoder CLI 1.0.45 acknowledges initialization with
			// system/init, while the QoderWork build uses control_response.
			// Both are official stream-json handshakes.
			if event.Type == "system" && event.Subtype == "init" {
				return nil
			}
		}
	}
}

func consumeEvents(ctx context.Context, events <-chan qoderEvent, readErrors <-chan error, toolCalls <-chan ToolCall, invocation Invocation, onText TextHandler, respondControl func(qoderEvent) error) (Result, error) {
	result := Result{Model: invocation.Model, FinishReason: "stop"}
	streamed := false
	idle := time.NewTimer(qoderStreamIdleTimeout)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-idle.C:
			return Result{}, fmt.Errorf("qodercli stream idle timeout after %s", qoderStreamIdleTimeout)
		case call := <-toolCalls:
			result.ToolCalls = []ToolCall{call}
			result.FinishReason = "tool_calls"
			return finalizeUsage(result, invocation), nil
		case err := <-readErrors:
			if errors.Is(err, io.EOF) && (result.Text != "" || len(result.ToolCalls) > 0) {
				return finalizeUsage(result, invocation), nil
			}
			return Result{}, err
		case event, ok := <-events:
			if !ok {
				return Result{}, io.ErrUnexpectedEOF
			}
			resetTimer(idle, qoderStreamIdleTimeout)
			if event.Type == "control_request" && event.controlRequestType() == "can_use_tool" {
				// Some Qoder builds emit caller tools as a bare can_use_tool request
				// instead of invoking the MCP bridge. Convert only tools declared by
				// the upstream harness into a normal tool call; never authorize Qoder
				// to execute the tool itself.
				call, keep, callErr := event.externalToolCall(invocation.Tools)
				if isWrappedToolName(event.Request.ToolName) && (callErr != nil || !keep) {
					if respondControl == nil {
						return Result{}, fmt.Errorf("qodercli requested generic external tool %q without a control responder", event.Request.ToolName)
					}
					if err := respondControl(event); err != nil {
						return Result{}, fmt.Errorf("allow qodercli generic external tool %q: %w", event.Request.ToolName, err)
					}
					continue
				}
				if callErr != nil {
					return Result{}, callErr
				}
				if !keep {
					return Result{}, fmt.Errorf("qodercli requested undeclared external tool %q", event.Request.ToolName)
				}
				result.ToolCalls = []ToolCall{call}
				result.FinishReason = "tool_calls"
				return finalizeUsage(result, invocation), nil
			}
			if event.Type == "assistant" && strings.TrimSpace(event.Error) != "" {
				return Result{}, fmt.Errorf("qodercli request failed: %s", strings.TrimSpace(event.Error))
			}
			if delta := textDelta(event); delta != "" {
				streamed = true
				if err := onText(delta); err != nil {
					return Result{}, err
				}
			}
			switch event.Type {
			case "assistant":
				// With caller tools, the authenticated MCP callback is the source of
				// truth. Qoder's assistant stream can contain lossy wrapper names such
				// as mcp__openai_tools or tool_calls, so never race those artifacts
				// against the exact tools/call payload from the bridge.
				text, calls, usage, err := assistantResult(event, invocation.Tools, len(invocation.Tools.Specs) == 0)
				if err != nil {
					return Result{}, err
				}
				if text != "" {
					result.Text = text
					if !streamed {
						if err = onText(text); err != nil {
							return Result{}, err
						}
					}
				}
				if usage.TotalTokens > 0 {
					result.Usage = usage
				}
				if len(calls) > 0 {
					result.ToolCalls = calls
					result.FinishReason = "tool_calls"
					return finalizeUsage(result, invocation), nil
				}
			case "result":
				if event.IsError || event.Subtype != "success" {
					detail := resultError(event)
					if detail == "" {
						detail = strings.TrimSpace(event.Result)
					}
					if detail == "" {
						detail = "unknown error"
					}
					return Result{}, fmt.Errorf("qodercli request failed: %s", detail)
				}
				if event.Result != "" {
					result.Text = event.Result
					if !streamed {
						if err := onText(event.Result); err != nil {
							return Result{}, err
						}
					}
				}
				if event.Usage.InputTokens+event.Usage.OutputTokens > 0 {
					result.Usage = usageFromQoder(event.Usage)
				}
				if invocation.Tools.Required {
					return Result{}, fmt.Errorf("qoder did not return the required external tool call")
				}
				return finalizeUsage(result, invocation), nil
			}
		}
	}
}

func allowGenericToolControlRequest(encoder *json.Encoder, event qoderEvent) error {
	requestID := strings.TrimSpace(event.RequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(event.Request.RequestID)
	}
	if requestID == "" {
		return fmt.Errorf("control request id is required")
	}
	input := event.Request.Input
	if len(input) == 0 || string(input) == "null" {
		input = event.Request.Arguments
	}
	if len(input) == 0 || string(input) == "null" {
		input = json.RawMessage(`{}`)
	}
	response := map[string]any{
		"behavior":     "allow",
		"updatedInput": input,
	}
	if toolUseID := strings.TrimSpace(event.Request.ToolUseID); toolUseID != "" {
		response["toolUseID"] = toolUseID
	}
	return encoder.Encode(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   response,
		},
	})
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func finalizeUsage(result Result, invocation Invocation) Result {
	if result.Usage.TotalTokens == 0 {
		result.Usage.PromptTokens = int64((len(invocation.SystemPrompt) + len(invocation.Prompt) + 3) / 4)
		result.Usage.CompletionTokens = int64((len(result.Text) + 3) / 4)
		for _, call := range result.ToolCalls {
			result.Usage.CompletionTokens += int64((len(call.Name) + len(call.Arguments) + 3) / 4)
		}
		result.Usage.TotalTokens = result.Usage.PromptTokens + result.Usage.CompletionTokens
	}
	return result
}

func stderrText(mu *sync.Mutex, value *strings.Builder) string {
	mu.Lock()
	defer mu.Unlock()
	return strings.TrimSpace(value.String())
}

func withStderr(err error, stderr string) error {
	if stderr == "" || strings.Contains(err.Error(), stderr) {
		return err
	}
	if len(stderr) > 2000 {
		stderr = stderr[len(stderr)-2000:]
	}
	return fmt.Errorf("%w: %s", err, stderr)
}

func DiscoverModels(ctx context.Context, account Account) ([]string, error) {
	if len(account.Models) > 0 {
		return append([]string(nil), account.Models...), nil
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := []string{"--list-models"}
	if account.ConfigDir != "" {
		args = append(args, "--config-dir", account.ConfigDir)
	}
	cmd := exec.CommandContext(discoveryCtx, account.CLIPath, args...)
	cmd.Env = cleanEnvironment(account)
	if account.CWD != "" {
		cmd.Dir = account.CWD
	} else {
		cmd.Dir = os.TempDir()
	}
	raw, err := cmd.CombinedOutput()
	if err != nil {
		// Model discovery is advisory. Aria and Cantus are service-side
		// compatibility names and must remain routable even when --list-models
		// is stale, rate-limited, or unavailable for one account.
		return append([]string(nil), qoderCompatibilityModels...), nil
	}
	return parseDiscoveredModels(string(raw)), nil
}

// Aria and Cantus are compatibility model names controlled by the Qoder
// service. Recent CLI releases may omit them from --list-models even when an
// account can still use them, so the gateway must not reject either name
// before qodercli has a chance to handle the request.
var qoderCompatibilityModels = []string{"Aria", "Cantus"}

func parseDiscoveredModels(raw string) []string {
	models := make([]string, 0)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "-")
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "model") || strings.Contains(strings.ToLower(line), "available model") {
			continue
		}
		models = append(models, line)
	}
	models = append(models, qoderCompatibilityModels...)
	return stringList(anyStrings(models))
}

func anyStrings(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func BridgeBesidePlugin(pluginPath string) string {
	return filepath.Join(filepath.Dir(pluginPath), "qoder-mcp-bridge")
}
