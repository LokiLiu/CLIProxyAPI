package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
static const cliproxy_host_api* stored_host;
static void store_host_api(const cliproxy_host_api* host) { stored_host = host; }
static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) return 1;
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}
static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) stored_host->free_buffer(ptr, len);
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/contrib/qoder-cli-provider/internal/provider"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const pluginIdentifier = "qoder"

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	AuthProvider          bool     `json:"auth_provider"`
	ModelProvider         bool     `json:"model_provider"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats"`
	ExecutorOutputFormats []string `json:"executor_output_formats"`
}

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID string `json:"stream_id,omitempty"`
}

type streamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
}

type streamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required", false, 0))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleMethod(C.GoString(method), requestBytes)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error(), retryableError(err), http.StatusBadGateway))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginIdentifier})
	case pluginabi.MethodAuthParse:
		return parseAuth(request)
	case pluginabi.MethodAuthLoginStart, pluginabi.MethodAuthLoginPoll:
		return errorEnvelope("login_unsupported", "Run qodercli login with the selected --config-dir, then add its auth JSON to CLIProxyAPI", false, http.StatusNotImplemented), nil
	case pluginabi.MethodAuthRefresh:
		return refreshAuth(request)
	case pluginabi.MethodModelStatic:
		return okEnvelope(pluginapi.ModelResponse{Provider: pluginIdentifier})
	case pluginabi.MethodModelForAuth:
		return modelsForAuth(request)
	case pluginabi.MethodExecutorExecute:
		return execute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return executeStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return countTokens(request)
	case pluginabi.MethodExecutorHTTPRequest:
		return errorEnvelope("unsupported", "qoder provider does not expose arbitrary HTTP requests", false, http.StatusNotImplemented), nil
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, false, 0), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Qoder CLI Provider",
			Version:          "0.1.0",
			Author:           "LokiLiu",
			GitHubRepository: "https://github.com/LokiLiu/CLIProxyAPI",
			ConfigFields:     []pluginapi.ConfigField{},
		},
		Capabilities: registrationCapability{
			AuthProvider:          true,
			ModelProvider:         true,
			Executor:              true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeOAuth),
			ExecutorInputFormats:  []string{"openai"},
			ExecutorOutputFormats: []string{"openai"},
		},
	}
}

func parseAuth(raw []byte) ([]byte, error) {
	var request pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	account, handled, err := provider.ParseAccount(request.RawJSON, request.FileName)
	if err != nil {
		return nil, err
	}
	if !handled {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	auth := authData(account, request.FileName)
	return okEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: auth})
}

func refreshAuth(raw []byte) ([]byte, error) {
	var request pluginapi.AuthRefreshRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	account, handled, err := provider.ParseAccount(request.StorageJSON, request.AuthID+".json")
	if err != nil {
		return nil, err
	}
	if !handled {
		return nil, fmt.Errorf("auth %q is not a qoder account", request.AuthID)
	}
	return okEnvelope(pluginapi.AuthRefreshResponse{Auth: authData(account, request.AuthID+".json")})
}

func authData(account provider.Account, fileName string) pluginapi.AuthData {
	return pluginapi.AuthData{
		Provider:    pluginIdentifier,
		ID:          account.ID,
		FileName:    fileName,
		Label:       account.Label,
		Prefix:      account.Prefix,
		StorageJSON: append([]byte(nil), account.Raw...),
		Metadata: map[string]any{
			"type":       "qoder",
			"cli_path":   account.CLIPath,
			"config_dir": account.ConfigDir,
			"label":      account.Label,
		},
		Attributes: map[string]string{"provider": pluginIdentifier, "prefix": account.Prefix},
	}
}

func modelsForAuth(raw []byte) ([]byte, error) {
	var request pluginapi.AuthModelRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	account, err := accountFromStorage(request.StorageJSON, request.AuthID)
	if err != nil {
		return nil, err
	}
	models, err := provider.DiscoverModels(context.Background(), account)
	if err != nil {
		return nil, err
	}
	infos := make([]pluginapi.ModelInfo, 0, len(models))
	for _, model := range models {
		infos = append(infos, pluginapi.ModelInfo{
			ID: model, Name: model, Object: "model", OwnedBy: account.Prefix,
			DisplayName: model, SupportedGenerationMethods: []string{"chat"},
			SupportedParameters:      []string{"max_tokens", "stream", "system", "tools", "tool_choice"},
			SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"},
			UserDefined: true,
		})
	}
	return okEnvelope(pluginapi.ModelResponse{Provider: pluginIdentifier, Models: infos})
}

func execute(raw []byte) ([]byte, error) {
	var request pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	account, invocation, err := executionInput(request)
	if err != nil {
		return nil, err
	}
	result, err := provider.Run(context.Background(), account, invocation, nil)
	if err != nil {
		return nil, err
	}
	payload, err := provider.EncodeResponse(result)
	if err != nil {
		return nil, err
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: payload, Headers: http.Header{"Content-Type": []string{"application/json"}}})
}

func executeStream(raw []byte) ([]byte, error) {
	var request rpcExecutorRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.StreamID) == "" {
		return errorEnvelope("executor_error", "stream_id is required", false, 0), nil
	}
	account, invocation, err := executionInput(request.ExecutorRequest)
	if err != nil {
		return nil, err
	}
	go runStream(request.StreamID, account, invocation)
	return okEnvelope(map[string]any{"headers": http.Header{"Content-Type": []string{"text/event-stream"}}})
}

func runStream(streamID string, account provider.Account, invocation provider.Invocation) {
	completionID := fmt.Sprintf("chatcmpl_%x", time.Now().UnixNano())
	if err := emitStream(streamID, provider.EncodeStreamRole(completionID, invocation.Model)); err != nil {
		closeStream(streamID, err)
		return
	}
	result, err := provider.Run(context.Background(), account, invocation, func(text string) error {
		return emitStream(streamID, provider.EncodeStreamText(completionID, invocation.Model, text))
	})
	if err != nil {
		closeStream(streamID, err)
		return
	}
	err = emitStream(streamID, provider.EncodeStreamFinish(completionID, result))
	closeStream(streamID, err)
}

func executionInput(request pluginapi.ExecutorRequest) (provider.Account, provider.Invocation, error) {
	if request.Format != "" && request.Format != "openai" {
		return provider.Account{}, provider.Invocation{}, fmt.Errorf("unsupported executor format %q", request.Format)
	}
	account, err := accountFromStorage(request.StorageJSON, request.AuthID)
	if err != nil {
		return provider.Account{}, provider.Invocation{}, err
	}
	model := strings.TrimSpace(request.Model)
	for _, prefix := range []string{account.Prefix + "/", "qoder/", "qoderwork/", "qoder2/"} {
		model = strings.TrimPrefix(model, prefix)
	}
	payload := request.Payload
	if len(payload) == 0 {
		payload = request.OriginalRequest
	}
	invocation, err := provider.BuildInvocation(payload, model)
	return account, invocation, err
}

func accountFromStorage(raw []byte, authID string) (provider.Account, error) {
	account, handled, err := provider.ParseAccount(raw, authID+".json")
	if err != nil {
		return provider.Account{}, err
	}
	if !handled {
		return provider.Account{}, fmt.Errorf("auth %q is not a qoder account", authID)
	}
	if account.BridgePath == "" {
		if executable, executableErr := os.Executable(); executableErr == nil {
			candidate := provider.BridgeBesidePlugin(executable)
			if _, statErr := os.Stat(candidate); statErr == nil {
				account.BridgePath = candidate
			}
		}
	}
	return account, nil
}

func countTokens(raw []byte) ([]byte, error) {
	var request pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	payload := request.Payload
	if len(payload) == 0 {
		payload = request.OriginalRequest
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(fmt.Sprintf(`{"total_tokens":%d}`, (len(payload)+3)/4))})
}

func emitStream(streamID string, payload []byte) error {
	_, err := callHost(pluginabi.MethodHostStreamEmit, streamEmitRequest{StreamID: streamID, Payload: payload})
	return err
}

func closeStream(streamID string, err error) {
	message := ""
	if err != nil {
		message = err.Error()
	}
	_, _ = callHost(pluginabi.MethodHostStreamClose, streamCloseRequest{StreamID: streamID, Error: message})
}

func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	cPayload := C.CBytes(rawPayload)
	if cPayload == nil {
		return nil, fmt.Errorf("allocate host callback %s", method)
	}
	defer C.free(cPayload)
	var response C.cliproxy_buffer
	code := C.call_host_api(cMethod, (*C.uint8_t)(cPayload), C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if code != 0 || len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s failed with code %d", method, int(code))
	}
	var env envelope
	if err = json.Unmarshal(rawResponse, &env); err != nil {
		return nil, err
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	return env.Result, nil
}

func okEnvelope(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string, retryable bool, status int) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message, Retryable: retryable, HTTPStatus: status}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr != nil {
		response.ptr = ptr
		response.len = C.size_t(len(raw))
	}
}

func retryableError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "intention_rejected") || strings.Contains(message, "requested_range_not_satisfiable")
}
