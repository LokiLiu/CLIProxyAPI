package main

import (
	"errors"
	"net/http"
	"testing"
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
