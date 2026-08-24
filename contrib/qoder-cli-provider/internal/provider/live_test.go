package provider

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestQoderLive(t *testing.T) {
	cliPath := os.Getenv("QODER_LIVE_TEST")
	if cliPath == "" {
		t.Skip("set QODER_LIVE_TEST to a logged-in qodercli path")
	}
	model := os.Getenv("QODER_LIVE_MODEL")
	if model == "" {
		model = "Lite"
	}
	invocation, err := BuildInvocation([]byte(`{
  "model":"Lite",
  "messages":[{"role":"user","content":"Reply with exactly LIVE_OK"}],
  "stream":true
}`), model)
	if err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	result, err := Run(context.Background(), Account{ID: "live", CLIPath: cliPath}, invocation, func(text string) error {
		streamed.WriteString(text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "LIVE_OK") || streamed.Len() == 0 {
		t.Fatalf("result=%q streamed=%q", result.Text, streamed.String())
	}
}

func TestQoderToolLive(t *testing.T) {
	cliPath := os.Getenv("QODER_LIVE_TEST")
	bridgePath := os.Getenv("QODER_LIVE_BRIDGE")
	if cliPath == "" || bridgePath == "" {
		t.Skip("set QODER_LIVE_TEST and QODER_LIVE_BRIDGE")
	}
	model := os.Getenv("QODER_LIVE_MODEL")
	if model == "" {
		model = "Lite"
	}
	invocation, err := BuildInvocation([]byte(`{
  "model":"Lite",
  "messages":[{"role":"user","content":"Use echo_number with value 42."}],
  "tools":[{"type":"function","function":{"name":"echo_number","description":"Echo a number","parameters":{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"]}}}],
  "tool_choice":"required"
}`), model)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), Account{ID: "live", CLIPath: cliPath, BridgePath: bridgePath}, invocation, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.FinishReason != "tool_calls" || len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "echo_number" {
		t.Fatalf("unexpected result: %#v", result)
	}
}
