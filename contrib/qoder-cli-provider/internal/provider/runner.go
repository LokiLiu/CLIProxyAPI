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
)

type TextHandler func(string) error

func Run(ctx context.Context, account Account, invocation Invocation, onText TextHandler) (Result, error) {
	if onText == nil {
		onText = func(string) error { return nil }
	}
	args, cleanup, errArgs := commandArgs(account, invocation)
	if errArgs != nil {
		return Result{}, errArgs
	}
	defer cleanup()

	cmd := exec.CommandContext(ctx, account.CLIPath, args...)
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
		_ = cmd.Process.Kill()
		return Result{}, fmt.Errorf("initialize qodercli: %w", err)
	}

	events := make(chan qoderEvent)
	readErrors := make(chan error, 1)
	go readEvents(stdout, events, readErrors)
	if err = waitForInitialize(ctx, events, readErrors); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		<-stderrDone
		return Result{}, withStderr(err, stderrText(&stderrMu, &stderrBuffer))
	}
	if err = encoder.Encode(userRequest(invocation.Prompt)); err != nil {
		_ = cmd.Process.Kill()
		return Result{}, fmt.Errorf("send qodercli request: %w", err)
	}

	result, runErr := consumeEvents(ctx, events, readErrors, invocation, onText)
	_ = stdin.Close()
	if runErr != nil || len(result.ToolCalls) > 0 {
		_ = cmd.Process.Kill()
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

func commandArgs(account Account, invocation Invocation) ([]string, func(), error) {
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
	if invocation.MaxTokens > 0 {
		args = append(args, "--max-output-tokens", fmt.Sprint(invocation.MaxTokens))
	}
	if account.ConfigDir != "" {
		args = append(args, "--config-dir", account.ConfigDir)
	}
	if len(invocation.Tools.Specs) == 0 {
		return args, func() {}, nil
	}
	if account.BridgePath == "" {
		return nil, func() {}, fmt.Errorf("qoder auth %q requires bridge_path when tools are supplied", account.ID)
	}
	configFile, err := writeMCPConfig(account.BridgePath, invocation.Tools.Specs)
	if err != nil {
		return nil, func() {}, err
	}
	args = append(args,
		"--mcp-config", configFile,
		"--strict-mcp-config",
		"--allowed-mcp-server-names", "openai_tools",
	)
	for _, spec := range invocation.Tools.Specs {
		args = append(args, "--allowed-tools", "mcp__openai_tools__"+spec.SDKName)
	}
	return args, func() { _ = os.Remove(configFile) }, nil
}

func writeMCPConfig(bridgePath string, tools []ToolSpec) (string, error) {
	rawTools, err := json.Marshal(tools)
	if err != nil {
		return "", fmt.Errorf("encode MCP tools: %w", err)
	}
	config := map[string]any{"mcpServers": map[string]any{
		"openai_tools": map[string]any{
			"command": bridgePath,
			"args":    []string{},
			"env": map[string]string{
				"CLI_PROXY_EXTERNAL_TOOLS": base64.RawURLEncoding.EncodeToString(rawTools),
			},
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
		}
	}
}

func consumeEvents(ctx context.Context, events <-chan qoderEvent, readErrors <-chan error, invocation Invocation, onText TextHandler) (Result, error) {
	result := Result{Model: invocation.Model, FinishReason: "stop"}
	streamed := false
	for {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case err := <-readErrors:
			if errors.Is(err, io.EOF) && (result.Text != "" || len(result.ToolCalls) > 0) {
				return finalizeUsage(result, invocation), nil
			}
			return Result{}, err
		case event, ok := <-events:
			if !ok {
				return Result{}, io.ErrUnexpectedEOF
			}
			if delta := textDelta(event); delta != "" {
				streamed = true
				if err := onText(delta); err != nil {
					return Result{}, err
				}
			}
			switch event.Type {
			case "assistant":
				text, calls, usage, err := assistantResult(event, invocation.Tools)
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
				if event.Subtype != "success" {
					return Result{}, fmt.Errorf("qodercli request failed: %s", resultError(event))
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
	args := []string{"--list-models"}
	if account.ConfigDir != "" {
		args = append(args, "--config-dir", account.ConfigDir)
	}
	cmd := exec.CommandContext(ctx, account.CLIPath, args...)
	cmd.Env = cleanEnvironment(account)
	if account.CWD != "" {
		cmd.Dir = account.CWD
	} else {
		cmd.Dir = os.TempDir()
	}
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list qoder models: %w: %s", err, strings.TrimSpace(string(raw)))
	}
	models := make([]string, 0)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "-")
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "model") || strings.Contains(strings.ToLower(line), "available model") {
			continue
		}
		models = append(models, line)
	}
	return stringList(anyStrings(models)), nil
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
