package provider

import (
	"encoding/json"
	"testing"
)

func TestEncodeAnthropicResponseUsesNativeToolBlock(t *testing.T) {
	raw, err := EncodeAnthropicResponse(Result{
		Model: "Aria",
		Text:  "checking",
		ToolCalls: []ToolCall{{
			ID: "toolu_1", Name: "bash", Arguments: json.RawMessage(`{"command":"pwd"}`),
		}},
		Usage: Usage{PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14},
	})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Type       string           `json:"type"`
		StopReason string           `json:"stop_reason"`
		Content    []map[string]any `json:"content"`
		Usage      struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "message" || response.StopReason != "tool_use" || len(response.Content) != 2 {
		t.Fatalf("unexpected response: %s", raw)
	}
	if response.Content[1]["type"] != "tool_use" || response.Content[1]["name"] != "bash" {
		t.Fatalf("unexpected tool block: %#v", response.Content[1])
	}
	if response.Usage.InputTokens != 10 || response.Usage.OutputTokens != 4 {
		t.Fatalf("unexpected usage: %#v", response.Usage)
	}
}

func TestEncodeAnthropicStreamEventIncludesSSEEventName(t *testing.T) {
	raw := EncodeAnthropicStreamEvent("message_stop", map[string]any{"type": "message_stop"})
	want := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	if string(raw) != want {
		t.Fatalf("stream event = %q, want %q", raw, want)
	}
}
