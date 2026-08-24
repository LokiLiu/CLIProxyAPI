package kiroprovider

import (
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/contrib/qoder-cli-provider/internal/provider"
)

func TestCallbackReadinessRequiresTheRequestSecret(t *testing.T) {
	callback, err := newToolCallback(provider.ToolPlan{})
	if err != nil {
		t.Fatal(err)
	}
	defer callback.Close()

	request, _ := http.NewRequest(http.MethodPost, callback.ReadyURL, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unexpected status for unauthenticated readiness: %d", response.StatusCode)
	}
	select {
	case <-callback.Ready:
		t.Fatal("unauthenticated callback marked the MCP server ready")
	default:
	}

	request, _ = http.NewRequest(http.MethodPost, callback.ReadyURL, nil)
	request.Header.Set("X-CLI-Proxy-Tool-Secret", callback.Secret)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected readiness status: %d", response.StatusCode)
	}
	select {
	case <-callback.Ready:
	case <-time.After(time.Second):
		t.Fatal("authenticated callback did not mark the MCP server ready")
	}
}
