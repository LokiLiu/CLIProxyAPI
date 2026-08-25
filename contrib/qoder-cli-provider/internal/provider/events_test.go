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

func TestNormalizeToolCallMapsEquivalentPropertyNames(t *testing.T) {
	plan := ToolPlan{
		Specs: []ToolSpec{{Name: "job_output", SDKName: "job_output", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{
				"job_id":     map[string]any{"type": "string"},
				"timeout_ms": map[string]any{"type": "integer"},
			}, "required": []any{"job_id"},
		}}},
		NameBySDK: map[string]string{"job_output": "job_output"},
	}
	call, keep, err := normalizeToolCall(qoderBlock{
		Type: "tool_use", Name: "mcp__openai_tools__job_output",
		Input: json.RawMessage(`{"jobId":"bash-35","timeoutMs":"600000"}`),
	}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !keep || call.Name != "job_output" || string(call.Arguments) != `{"job_id":"bash-35","timeout_ms":600000}` {
		t.Fatalf("unexpected call: %#v", call)
	}
}

func TestNormalizeToolCallUnwrapsConcreteToolArgumentEnvelope(t *testing.T) {
	plan := ToolPlan{
		Specs: []ToolSpec{{Name: "job_output", SDKName: "job_output", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{
				"job_id": map[string]any{"type": "string"},
			}, "required": []any{"job_id"},
		}}},
		NameBySDK: map[string]string{"job_output": "job_output"},
	}
	call, keep, err := normalizeToolCall(qoderBlock{
		Type: "tool_use", Name: "mcp__openai_tools__job_output",
		Input: json.RawMessage(`{"name":"job_output","arguments":"{\"job_id\":\"bash-35\"}"}`),
	}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !keep || string(call.Arguments) != `{"job_id":"bash-35"}` {
		t.Fatalf("unexpected call: %#v", call)
	}
}

func TestNormalizeToolCallDoesNotUnwrapDeclaredEnvelopeProperty(t *testing.T) {
	plan := ToolPlan{
		Specs: []ToolSpec{{Name: "send", SDKName: "send", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{
				"input": map[string]any{"type": "object", "properties": map[string]any{
					"value_id": map[string]any{"type": "string"},
				}},
			}, "required": []any{"input"},
		}}},
		NameBySDK: map[string]string{"send": "send"},
	}
	call, keep, err := normalizeToolCall(qoderBlock{
		Type: "tool_use", Name: "mcp__openai_tools__send",
		Input: json.RawMessage(`{"input":{"valueId":"v1"}}`),
	}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !keep || string(call.Arguments) != `{"input":{"value_id":"v1"}}` {
		t.Fatalf("unexpected call: %#v", call)
	}
}

func TestNormalizeToolCallPassesAnthropicArgumentsWithoutCoercion(t *testing.T) {
	plan := ToolPlan{
		Specs: []ToolSpec{{Name: "bash", SDKName: "bash", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"timeoutMs": map[string]any{"type": "integer"}},
		}}},
		NameBySDK:   map[string]string{"bash": "bash"},
		PassThrough: true,
	}
	call, keep, err := normalizeToolCall(qoderBlock{
		Type: "tool_use", ID: "toolu_1", Name: "mcp__openai_tools__bash",
		Input: json.RawMessage(`{"timeoutMs":"1200"}`),
	}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !keep || string(call.Arguments) != `{"timeoutMs":"1200"}` {
		t.Fatalf("unexpected pass-through call: %#v", call)
	}
}
