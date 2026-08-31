package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWaitForInitializeAcceptsPersonalQoderSystemInit(t *testing.T) {
	events := make(chan qoderEvent, 1)
	errs := make(chan error, 1)
	events <- qoderEvent{Type: "system", Subtype: "init"}
	if err := waitForInitialize(context.Background(), events, errs); err != nil {
		t.Fatalf("waitForInitialize() error = %v", err)
	}
}

func TestConsumeEventsRejectsAuthenticationFailure(t *testing.T) {
	events := make(chan qoderEvent, 1)
	errs := make(chan error, 1)
	events <- qoderEvent{Type: "assistant", Error: "authentication_failed"}
	_, err := consumeEvents(context.Background(), events, errs, nil, Invocation{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "authentication_failed") {
		t.Fatalf("consumeEvents() error = %v", err)
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("authentication failure was reduced to EOF: %v", err)
	}
}

func TestConsumeEventsForwardsPermissionRequestAsCallerTool(t *testing.T) {
	events := make(chan qoderEvent, 1)
	event := qoderEvent{Type: "control_request"}
	event.Request.Subtype = "can_use_tool"
	event.Request.ToolName = "quick_search"
	event.Request.ToolUseID = "call-search-1"
	event.Request.Input = json.RawMessage(`{"query":"wheel build","time_range":"OneMonth"}`)
	events <- event

	result, err := consumeEvents(context.Background(), events, nil, nil, Invocation{
		Model: "Aria",
		Tools: ToolPlan{
			Specs:       []ToolSpec{{Name: "quick_search", SDKName: "quick_search"}},
			NameBySDK:   map[string]string{"quick_search": "quick_search"},
			PassThrough: true,
		},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.FinishReason != "tool_calls" || len(result.ToolCalls) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	call := result.ToolCalls[0]
	if call.ID != "call-search-1" || call.Name != "quick_search" || string(call.Arguments) != `{"query":"wheel build","time_range":"OneMonth"}` {
		t.Fatalf("unexpected forwarded tool call: %#v", call)
	}
}

func TestConsumeEventsAllowsGenericPermissionAndWaitsForBridgeCallback(t *testing.T) {
	for _, wrapper := range []string{"mcp__openai_tools", "[]"} {
		t.Run(wrapper, func(t *testing.T) {
			events := make(chan qoderEvent, 2)
			toolCalls := make(chan ToolCall, 1)
			event := qoderEvent{Type: "control_request", RequestID: "permission-1"}
			event.Request.Subtype = "can_use_tool"
			event.Request.ToolName = wrapper
			event.Request.ToolUseID = "wrapper-1"
			event.Request.Input = json.RawMessage(`{}`)
			events <- event

			responded := false
			result, err := consumeEvents(context.Background(), events, nil, toolCalls, Invocation{
				Model: "Aria",
				Tools: ToolPlan{
					Specs:       []ToolSpec{{Name: "bash", SDKName: "bash"}},
					NameBySDK:   map[string]string{"bash": "bash"},
					PassThrough: true,
				},
			}, nil, func(got qoderEvent) error {
				responded = true
				if got.RequestID != "permission-1" {
					t.Fatalf("unexpected control request: %#v", got)
				}
				events <- qoderEvent{Type: "result", Subtype: "success"}
				toolCalls <- ToolCall{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"command":"echo ok"}`)}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !responded || result.FinishReason != "tool_calls" || len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "bash" {
				t.Fatalf("responded=%v result=%#v", responded, result)
			}
		})
	}
}

func TestConsumeEventsDoesNotReturnEmptyResultWhileGenericCallbackIsPending(t *testing.T) {
	events := make(chan qoderEvent, 2)
	errs := make(chan error, 1)
	event := qoderEvent{Type: "control_request", RequestID: "permission-1"}
	event.Request.Subtype = "can_use_tool"
	event.Request.ToolName = "mcp__openai_tools"
	event.Request.Input = json.RawMessage(`{}`)
	events <- event

	_, err := consumeEvents(context.Background(), events, errs, make(chan ToolCall), Invocation{
		Tools: ToolPlan{
			Specs:       []ToolSpec{{Name: "bash", SDKName: "bash"}},
			NameBySDK:   map[string]string{"bash": "bash"},
			PassThrough: true,
		},
	}, nil, func(qoderEvent) error {
		events <- qoderEvent{Type: "result", Subtype: "success"}
		errs <- io.EOF
		return nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("consumeEvents() error = %v, want EOF instead of empty success", err)
	}
}

func TestAllowGenericToolControlRequestWritesSDKResponse(t *testing.T) {
	event := qoderEvent{Type: "control_request", RequestID: "permission-1"}
	event.Request.ToolUseID = "wrapper-1"
	event.Request.Input = json.RawMessage(`{"command":"echo ok"}`)
	var output bytes.Buffer
	if err := allowGenericToolControlRequest(json.NewEncoder(&output), event); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	response, _ := got["response"].(map[string]any)
	permission, _ := response["response"].(map[string]any)
	if got["type"] != "control_response" || response["subtype"] != "success" || response["request_id"] != "permission-1" || permission["behavior"] != "allow" || permission["toolUseID"] != "wrapper-1" {
		t.Fatalf("unexpected response: %#v", got)
	}
	updated, _ := permission["updatedInput"].(map[string]any)
	if updated["command"] != "echo ok" {
		t.Fatalf("unexpected updated input: %#v", updated)
	}
}

func TestToolCallbackPreservesExactToolNameAndArguments(t *testing.T) {
	plan := ToolPlan{
		Specs:       []ToolSpec{{Name: "job_output", SDKName: "job_output"}},
		NameBySDK:   map[string]string{"job_output": "job_output"},
		PassThrough: true,
	}
	callback, err := newToolCallback(plan)
	if err != nil {
		t.Fatal(err)
	}
	defer callback.Close()

	request, err := http.NewRequest(http.MethodPost, callback.URL, strings.NewReader(`{"name":"job_output","input":{"job_id":"job-7","timeout_ms":"600000"}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-CLI-Proxy-Tool-Secret", callback.Secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("callback status = %d", response.StatusCode)
	}
	select {
	case call := <-callback.Calls:
		var arguments map[string]any
		if err = json.Unmarshal(call.Arguments, &arguments); err != nil {
			t.Fatal(err)
		}
		if call.Name != "job_output" || arguments["job_id"] != "job-7" || arguments["timeout_ms"] != "600000" {
			t.Fatalf("unexpected tool call: %#v %#v", call, arguments)
		}
	case <-time.After(time.Second):
		t.Fatal("tool callback was not delivered")
	}
}

func TestCommandArgsBypassPermissionsOnlyForCallerTools(t *testing.T) {
	withoutTools, cleanup, err := commandArgs(Account{}, Invocation{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if strings.Contains(strings.Join(withoutTools, " "), "--permission-mode") {
		t.Fatal("permission mode was enabled without caller tools")
	}

	callback, err := newToolCallback(ToolPlan{Specs: []ToolSpec{{Name: "bash", SDKName: "bash"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer callback.Close()
	withTools, cleanup, err := commandArgs(Account{ID: "test", BridgePath: "/tmp/bridge"}, Invocation{
		Tools: ToolPlan{Specs: []ToolSpec{{Name: "bash", SDKName: "bash"}}},
	}, callback)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if joined := strings.Join(withTools, " "); !strings.Contains(joined, "--permission-mode bypass_permissions") {
		t.Fatalf("caller tools did not enable non-interactive permissions: %s", joined)
	}
}

func TestCommandArgsWritesAndCleansImageAttachments(t *testing.T) {
	args, cleanup, err := commandArgs(Account{}, Invocation{Attachments: []Attachment{{
		FileName: "image-001.png", MediaType: "image/png", Data: []byte("png-data"),
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	attachmentPath := ""
	for index, arg := range args {
		if arg == "--attachment" && index+1 < len(args) {
			attachmentPath = args[index+1]
			break
		}
	}
	if attachmentPath == "" {
		cleanup()
		t.Fatalf("args did not contain --attachment: %#v", args)
	}
	data, err := os.ReadFile(attachmentPath)
	if err != nil || string(data) != "png-data" {
		cleanup()
		t.Fatalf("attachment data = %q, err=%v", data, err)
	}
	cleanup()
	if _, err = os.Stat(attachmentPath); !os.IsNotExist(err) {
		t.Fatalf("attachment still exists after cleanup: %v", err)
	}
}

func TestParseDiscoveredModelsAddsCompatibilityModels(t *testing.T) {
	got := parseDiscoveredModels("MODEL\nAuto\nPerformance\n")
	want := []string{"Auto", "Performance", "Aria", "Cantus"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDiscoveredModels() = %#v, want %#v", got, want)
	}
}

func TestParseDiscoveredModelsDoesNotDuplicateCompatibilityModels(t *testing.T) {
	got := parseDiscoveredModels("Available models:\n- Auto\n- Aria\n- Cantus\n")
	want := []string{"Auto", "Aria", "Cantus"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDiscoveredModels() = %#v, want %#v", got, want)
	}
}
