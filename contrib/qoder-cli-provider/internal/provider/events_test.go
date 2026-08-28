package provider

import (
	"encoding/json"
	"strings"
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

func TestNormalizeToolCallInfersConcreteToolFromGenericWrapper(t *testing.T) {
	plan := ToolPlan{
		Specs: []ToolSpec{
			{Name: "bash", SDKName: "bash", Parameters: map[string]any{
				"type": "object", "properties": map[string]any{
					"command":     map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
					"timeoutMs":   map[string]any{"type": "integer"},
				}, "required": []any{"command"},
			}},
			{Name: "job_output", SDKName: "job_output", Parameters: map[string]any{
				"type": "object", "properties": map[string]any{
					"job_id":     map[string]any{"type": "string"},
					"timeout_ms": map[string]any{"type": "integer"},
				}, "required": []any{"job_id"},
			}},
		},
		NameBySDK:   map[string]string{"bash": "bash", "job_output": "job_output"},
		PassThrough: true,
	}
	for _, wrapper := range []string{
		"mcp__openai_tools", "MCP-OpenAI-Tools",
		"tool_use", "ToolUse", "tool-use",
		"tool_call", "ToolCalls", "functionCall",
	} {
		t.Run(wrapper, func(t *testing.T) {
			call, keep, err := normalizeToolCall(qoderBlock{
				Type: "tool_use", ID: "call_1", Name: wrapper,
				Input: json.RawMessage(`{"command":"echo ok","description":"run command","timeoutMs":"30000"}`),
			}, plan)
			if err != nil {
				t.Fatal(err)
			}
			if !keep || call.Name != "bash" || string(call.Arguments) != `{"command":"echo ok","description":"run command","timeoutMs":"30000"}` {
				t.Fatalf("unexpected call: %#v", call)
			}
		})
	}
}

func TestNormalizeToolCallPreservesDeclaredToolNamedLikeWrapper(t *testing.T) {
	plan := ToolPlan{
		Specs: []ToolSpec{{Name: "ToolUse", SDKName: "ToolUse", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}},
		}}},
		NameBySDK: map[string]string{"ToolUse": "ToolUse"},
	}
	call, keep, err := normalizeToolCall(qoderBlock{
		Type: "tool_use", Name: "ToolUse", Input: json.RawMessage(`{"value":"literal tool"}`),
	}, plan)
	if err != nil || !keep || call.Name != "ToolUse" {
		t.Fatalf("call=%#v keep=%v err=%v", call, keep, err)
	}
}

func TestNormalizeToolCallDoesNotGuessAmbiguousBareMCPServerName(t *testing.T) {
	plan := ToolPlan{
		Specs: []ToolSpec{
			{Name: "first", SDKName: "first", Parameters: map[string]any{
				"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}},
			}},
			{Name: "second", SDKName: "second", Parameters: map[string]any{
				"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}},
			}},
		},
		NameBySDK:   map[string]string{"first": "first", "second": "second"},
		PassThrough: true,
	}
	_, keep, err := normalizeToolCall(qoderBlock{
		Type: "tool_use", Name: "mcp__openai_tools", Input: json.RawMessage(`{"value":"same shape"}`),
	}, plan)
	if err != nil || keep {
		t.Fatalf("keep=%v err=%v", keep, err)
	}
}

func TestNormalizeToolCallUnwrapsNestedFunctionCall(t *testing.T) {
	plan := ToolPlan{
		Specs: []ToolSpec{{Name: "bash", SDKName: "bash", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []any{"command"},
		}}},
		NameBySDK: map[string]string{"bash": "bash"},
	}
	call, keep, err := normalizeToolCall(qoderBlock{
		Type: "tool_use", Name: "function_call",
		Input: json.RawMessage(`{"function":{"name":"bash","arguments":{"command":"echo ok"}}}`),
	}, plan)
	if err != nil || !keep || call.Name != "bash" || string(call.Arguments) != `{"command":"echo ok"}` {
		t.Fatalf("call=%#v keep=%v err=%v", call, keep, err)
	}
}

func TestNormalizeToolCallIgnoresEmptyToolCallsMarker(t *testing.T) {
	_, keep, err := normalizeToolCall(qoderBlock{Type: "tool_use", Name: "tool_calls", Input: json.RawMessage(`{}`)}, ToolPlan{})
	if err != nil || keep {
		t.Fatalf("keep=%v err=%v", keep, err)
	}
}

func TestAssistantResultIgnoresLossyToolMarkersWhenMCPCallbackIsAuthoritative(t *testing.T) {
	event := qoderEvent{Type: "assistant", Message: qoderMessage{Content: []qoderBlock{{
		Type: "tool_use", Name: "tool_calls", Input: json.RawMessage(`{"name":"bash"}`),
	}}}}
	_, calls, _, err := assistantResult(event, ToolPlan{}, false)
	if err != nil || len(calls) != 0 {
		t.Fatalf("calls=%#v err=%v", calls, err)
	}
}

func TestControlPermissionRequestBecomesDeclaredExternalToolCall(t *testing.T) {
	var event qoderEvent
	err := json.Unmarshal([]byte(`{
		"type":"control_request",
		"request_id":"permission-1",
		"request":{
			"subtype":"can_use_tool",
			"tool_name":"quick_search",
			"tool_use_id":"call-search-1",
			"input":{"query":"wheel build","time_range":"OneMonth"}
		}
	}`), &event)
	if err != nil {
		t.Fatal(err)
	}
	plan := ToolPlan{
		Specs:       []ToolSpec{{Name: "quick_search", SDKName: "quick_search"}},
		NameBySDK:   map[string]string{"quick_search": "quick_search"},
		PassThrough: true,
	}
	call, keep, err := event.externalToolCall(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !keep || call.ID != "call-search-1" || call.Name != "quick_search" || string(call.Arguments) != `{"query":"wheel build","time_range":"OneMonth"}` {
		t.Fatalf("unexpected control-request tool call: %#v", call)
	}
}

func TestControlPermissionRequestRejectsUndeclaredTool(t *testing.T) {
	event := qoderEvent{Type: "control_request"}
	event.Request.Subtype = "can_use_tool"
	event.Request.ToolName = "quick_search"
	_, _, err := event.externalToolCall(ToolPlan{NameBySDK: map[string]string{"bash": "bash"}})
	if err == nil || !strings.Contains(err.Error(), "unknown external tool") {
		t.Fatalf("externalToolCall() error = %v", err)
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
