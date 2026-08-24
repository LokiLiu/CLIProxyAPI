package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNotifyReadyUsesConfiguredSecret(t *testing.T) {
	requests := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests <- request
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	t.Setenv("CLI_PROXY_MCP_READY_URL", server.URL)
	t.Setenv("CLI_PROXY_TOOL_CALLBACK_SECRET", "secret")

	notifyReady()
	request := <-requests
	if value := request.Header.Get("X-CLI-Proxy-Tool-Secret"); value != "secret" {
		t.Fatalf("unexpected callback secret %q", value)
	}
}
