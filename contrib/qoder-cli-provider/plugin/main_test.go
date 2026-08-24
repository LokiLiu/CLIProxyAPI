package main

import (
	"errors"
	"net/http"
	"testing"
)

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
