package provider

import (
	"encoding/json"
	"testing"
)

func TestNormalizeToolCallUnwrapsMCPNameAndCoercesNumbers(t *testing.T) {
	plan := ToolPlan{
		Specs: []ToolSpec{{
			Name: "bash", SDKName: "bash",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"timeoutMs": map[string]any{"type": "integer"},
			}},
		}},
		NameBySDK: map[string]string{"bash": "bash"},
	}
	call, keep, err := normalizeToolCall(qoderBlock{
		Type: "tool_use", ID: "call_1", Name: "mcp__openai_tools__bash",
		Input: json.RawMessage(`{"timeoutMs":"1200"}`),
	}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !keep || call.Name != "bash" || string(call.Arguments) != `{"timeoutMs":1200}` {
		t.Fatalf("unexpected call: %#v", call)
	}
}

func TestNormalizeToolCallIgnoresServerMarker(t *testing.T) {
	_, keep, err := normalizeToolCall(qoderBlock{Type: "tool_use", Name: "mcp__openai_tools", Input: json.RawMessage(`{}`)}, ToolPlan{})
	if err != nil || keep {
		t.Fatalf("keep=%v err=%v", keep, err)
	}
}

func TestNormalizeToolCallRejectsUnknownWrapper(t *testing.T) {
	_, _, err := normalizeToolCall(qoderBlock{Type: "tool_use", Name: "tool_calls", Input: json.RawMessage(`{"name":"bash"}`)}, ToolPlan{})
	if err == nil {
		t.Fatal("expected unknown wrapper to be rejected")
	}
}

func TestNormalizeToolCallRejectsInvalidArguments(t *testing.T) {
	plan := ToolPlan{
		Specs: []ToolSpec{{Name: "bash", SDKName: "bash", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"timeoutMs": map[string]any{"type": "integer"}}, "required": []any{"timeoutMs"},
		}}},
		NameBySDK: map[string]string{"bash": "bash"},
	}
	_, _, err := normalizeToolCall(qoderBlock{Type: "tool_use", Name: "mcp__openai_tools__bash", Input: json.RawMessage(`{"timeoutMs":"later"}`)}, plan)
	if err == nil {
		t.Fatal("expected invalid integer to be rejected")
	}
}
