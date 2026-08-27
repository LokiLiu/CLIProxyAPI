package main

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/contrib/qoder-cli-provider/internal/provider"
)

func TestPluginRegistrationSupportsNativeClaudeMessages(t *testing.T) {
	registration := pluginRegistration()
	if !containsString(registration.Capabilities.ExecutorInputFormats, "claude") || !containsString(registration.Capabilities.ExecutorOutputFormats, "claude") {
		t.Fatalf("registration formats = %#v -> %#v", registration.Capabilities.ExecutorInputFormats, registration.Capabilities.ExecutorOutputFormats)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestPluginErrorDetailsClassifiesInvalidModelAsBadRequest(t *testing.T) {
	code, status := pluginErrorDetails(errors.New(`EOF: Invalid model "Cantus".`))
	if code != "model_not_found" || status != http.StatusBadRequest {
		t.Fatalf("pluginErrorDetails() = (%q, %d), want (%q, %d)", code, status, "model_not_found", http.StatusBadRequest)
	}
}

func TestPluginErrorDetailsKeepsOtherFailuresAsBadGateway(t *testing.T) {
	code, status := pluginErrorDetails(errors.New("unexpected EOF"))
	if code != "plugin_error" || status != http.StatusBadGateway {
		t.Fatalf("pluginErrorDetails() = (%q, %d), want (%q, %d)", code, status, "plugin_error", http.StatusBadGateway)
	}
}

func TestPluginErrorDetailsClassifiesExhaustedModelQueueAsRetryableOverload(t *testing.T) {
	err := errors.New("qodercli request failed: model queue recovery attempts exceeded")
	code, status := pluginErrorDetails(err)
	if code != "provider_overloaded" || status != anthropicOverloadedStatus {
		t.Fatalf("pluginErrorDetails() = (%q, %d), want (%q, %d)", code, status, "provider_overloaded", anthropicOverloadedStatus)
	}
	if !retryableError(err) {
		t.Fatal("model queue exhaustion should be retryable")
	}
}

func TestPluginErrorDetailsKeepsMalformedToolOutputRequestScoped(t *testing.T) {
	for _, message := range []string{
		`qoder returned unknown external tool "tool_use"`,
		`qodercli requested undeclared external tool "mcp__openai_tools"`,
		`qoder returned invalid arguments for tool bash: timeoutMs must be integer`,
	} {
		err := errors.New(message)
		code, status := pluginErrorDetails(err)
		if code != "request_scoped" || status != http.StatusBadGateway {
			t.Fatalf("pluginErrorDetails(%q) = (%q, %d)", message, code, status)
		}
		if !retryableError(err) {
			t.Fatalf("retryableError(%q) = false", message)
		}
	}
}

func TestPluginErrorDetailsKeepsInvalidImageInputRequestScoped(t *testing.T) {
	err := errors.New(`unsupported image media type "image/svg+xml"`)
	code, status := pluginErrorDetails(err)
	if code != "request_scoped" || status != http.StatusBadRequest {
		t.Fatalf("pluginErrorDetails() = (%q, %d)", code, status)
	}
	if retryableError(err) {
		t.Fatal("invalid image input must not be retried")
	}
}

func TestPreflightRunReturnsFailureBeforeCommittingStream(t *testing.T) {
	want := errors.New("model queue recovery attempts exceeded")
	bootstrap, err := preflightRun(context.Background(), func(provider.TextHandler) (provider.Result, error) {
		return provider.Result{}, want
	})
	if !errors.Is(err, want) || bootstrap != nil {
		t.Fatalf("preflightRun() = (%#v, %v), want (nil, %v)", bootstrap, err, want)
	}
}

func TestPreflightRunBuffersFirstTextAndContinuesStreaming(t *testing.T) {
	wantResult := provider.Result{Model: "Aria", FinishReason: "stop"}
	bootstrap, err := preflightRun(context.Background(), func(onText provider.TextHandler) (provider.Result, error) {
		if errText := onText("first"); errText != nil {
			return provider.Result{}, errText
		}
		if errText := onText("second"); errText != nil {
			return provider.Result{}, errText
		}
		return wantResult, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	gotResult, err := bootstrap.consume(context.Background(), func(text string) error {
		texts = append(texts, text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(texts, []string{"first", "second"}) {
		t.Fatalf("streamed texts = %#v", texts)
	}
	if !reflect.DeepEqual(gotResult, wantResult) {
		t.Fatalf("result = %#v, want %#v", gotResult, wantResult)
	}
}
