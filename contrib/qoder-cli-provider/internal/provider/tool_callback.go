package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"
)

type toolCallbackServer struct {
	URL      string
	ReadyURL string
	Secret   string
	Calls    chan ToolCall
	Ready    chan struct{}
	server   *http.Server
	plan     ToolPlan
	once     sync.Once
}

func newToolCallback(plan ToolPlan) (*toolCallbackServer, error) {
	callback := &toolCallbackServer{
		Calls: make(chan ToolCall, 4),
		Ready: make(chan struct{}),
		plan:  plan,
	}
	if len(plan.Specs) == 0 {
		close(callback.Ready)
		return callback, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 24)
	if _, err = rand.Read(secret); err != nil {
		_ = listener.Close()
		return nil, err
	}
	baseURL := "http://" + listener.Addr().String()
	callback.URL = baseURL + "/tool"
	callback.ReadyURL = baseURL + "/ready"
	callback.Secret = hex.EncodeToString(secret)
	mux := http.NewServeMux()
	mux.HandleFunc("/tool", callback.handleTool)
	mux.HandleFunc("/ready", callback.handleReady)
	callback.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = callback.server.Serve(listener) }()
	return callback, nil
}

func (c *toolCallbackServer) authorized(request *http.Request) bool {
	return request.Method == http.MethodPost && request.Header.Get("X-CLI-Proxy-Tool-Secret") == c.Secret
}

func (c *toolCallbackServer) handleReady(response http.ResponseWriter, request *http.Request) {
	if !c.authorized(request) {
		http.NotFound(response, request)
		return
	}
	c.once.Do(func() { close(c.Ready) })
	response.WriteHeader(http.StatusNoContent)
}

func (c *toolCallbackServer) handleTool(response http.ResponseWriter, request *http.Request) {
	if !c.authorized(request) {
		http.NotFound(response, request)
		return
	}
	var body struct {
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	call, err := NormalizeExternalToolCall("", body.Name, body.Input, c.plan)
	if err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	select {
	case c.Calls <- call:
		response.WriteHeader(http.StatusAccepted)
	default:
		http.Error(response, "tool callback queue is full", http.StatusConflict)
	}
}

func (c *toolCallbackServer) Close() {
	if c != nil && c.server != nil {
		_ = c.server.Shutdown(context.Background())
	}
}
