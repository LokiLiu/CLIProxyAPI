package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
	close(events)
	errs <- io.EOF
	_, err := consumeEvents(context.Background(), events, errs, nil, Invocation{}, nil)
	if err == nil || !strings.Contains(err.Error(), "authentication_failed") {
		t.Fatalf("consumeEvents() error = %v", err)
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("authentication failure was reduced to EOF: %v", err)
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
