package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct { uint32_t abi_version; void* host_ctx; cliproxy_host_call_fn call; cliproxy_host_free_fn free_buffer; } cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct { uint32_t abi_version; cliproxy_plugin_call_fn call; cliproxy_plugin_free_fn free_buffer; cliproxy_plugin_shutdown_fn shutdown; } cliproxy_plugin_api;

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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"gopkg.in/yaml.v3"
)

const pluginIdentifier = "madapi-dynamic-executor"

const cockpitHeader = "X-MadAPI-Codex-Cockpit"

const (
	chatCompletionsPath = "/chat/completions"
	responsesPath       = "/responses"
	imagesGenerationsPath = "/images/generations"
	openAIImageFormat    = "openai-image"
	openAIResponsesFormat = "openai-response"
)

var errMadAPIEmptyStream = errors.New("MadAPI stream closed before a valid event")

var cockpitModelAliases = map[string]string{
	"gpt-5.5":       "claude-fable-5",
	"gpt-5.4":       "claude-opus-5",
	"gpt-5.6-sol":   "gpt-5.6-sol",
	"gpt-5.6-terra": "gpt-5.6-terra",
	"gpt-5.6-luna":  "gpt-5.6-luna",
	"gpt-5.4-mini":  "grok-4.5",
	"gpt-5.3-codex": "kimi-k3",
	"gpt-5.2":       "deepseek-v4-flash",
}

var currentConfig atomic.Value

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type pluginConfig struct {
	Enabled          bool   `yaml:"enabled"`
	BaseURL          string `yaml:"base_url"`
	BootstrapRetries int    `yaml:"bootstrap_retries"`
}

type registration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  capabilities       `json:"capabilities"`
}

type capabilities struct {
	ModelRouter           bool     `json:"model_router"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats"`
	ExecutorOutputFormats []string `json:"executor_output_formats"`
}

type rpcModelRouteRequest struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcHostHTTPRequest struct {
	HostCallbackID string       `json:"host_callback_id,omitempty"`
	Request        *httpRequest `json:"request,omitempty"`
}

type httpRequest struct {
	Method  string      `json:"method,omitempty"`
	URL     string      `json:"url,omitempty"`
	Headers http.Header `json:"headers,omitempty"`
	Body    []byte      `json:"body,omitempty"`
}

type rpcHostHTTPStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}

type rpcHostHTTPStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
}

type rpcStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
}

type rpcStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

type hostStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers,omitempty"`
	StreamID   string      `json:"stream_id,omitempty"`
}

type hostStreamChunk struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
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
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var rawRequest []byte
	if request != nil && requestLen > 0 {
		rawRequest = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleMethod(C.GoString(method), rawRequest)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
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
		if err := configure(request); err != nil {
			return nil, err
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodModelRoute:
		return routeModel(request)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginIdentifier})
	case pluginabi.MethodExecutorExecute:
		return execute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return executeStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":0}`)})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	var request lifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &request); err != nil {
			return err
		}
	}
	config := pluginConfig{}
	if len(request.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(request.ConfigYAML, &config); err != nil {
			return err
		}
	}
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if config.Enabled && config.BaseURL == "" {
		return fmt.Errorf("base_url is required when enabled")
	}
	currentConfig.Store(config)
	return nil
}

func loadedConfig() pluginConfig {
	if config, ok := currentConfig.Load().(pluginConfig); ok {
		return config
	}
	return pluginConfig{}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginIdentifier,
			Version:          "0.1.0",
			Author:           "MadAPI",
			GitHubRepository: "https://github.com/Mad12345-qw/mad-new-api",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable dynamic per-request MadAPI routing."},
				{Name: "base_url", Type: pluginapi.ConfigFieldTypeString, Description: "MadAPI OpenAI-compatible base URL."},
			},
		},
		Capabilities: capabilities{
			ModelRouter:           true,
			Executor:              true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeBoth),
			ExecutorInputFormats:  []string{openAIResponsesFormat, "chat-completions", openAIImageFormat},
			ExecutorOutputFormats: []string{openAIResponsesFormat, "chat-completions", openAIImageFormat},
		},
	}
}

func routeModel(raw []byte) ([]byte, error) {
	var request rpcModelRouteRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	config := loadedConfig()
	format := strings.ToLower(strings.TrimSpace(request.SourceFormat))
	if !config.Enabled || (format != "openai" && format != "openai-response" && format != openAIImageFormat) {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	if strings.TrimSpace(request.RequestedModel) == "" {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	return okEnvelope(pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf, Reason: "madapi_dynamic_authorization"})
}

func execute(raw []byte) ([]byte, error) {
	var request rpcExecutorRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	response, err := callMadAPI(request.ExecutorRequest, request.HostCallbackID, false)
	if err != nil {
		return errorEnvelope("executor_error", err.Error()), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: response.Body, Headers: response.Headers})
}

func executeStream(raw []byte) ([]byte, error) {
	var request rpcExecutorRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.StreamID) == "" {
		return errorEnvelope("executor_error", "stream_id is required"), nil
	}
	go func() {
		if err := forwardMadAPIStream(context.Background(), request); err != nil {
			closePluginStream(request.StreamID, err.Error())
			return
		}
		closePluginStream(request.StreamID, "")
	}()
	return okEnvelope(map[string]any{"headers": http.Header{"Content-Type": []string{"text/event-stream"}}})
}

func callMadAPI(request pluginapi.ExecutorRequest, callbackID string, stream bool) (pluginapi.HTTPResponse, error) {
	config := loadedConfig()
	if !config.Enabled || config.BaseURL == "" {
		return pluginapi.HTTPResponse{}, fmt.Errorf("dynamic MadAPI executor is not configured")
	}
	if stream {
		return pluginapi.HTTPResponse{}, fmt.Errorf("streaming requires forwardMadAPIStream")
	}
	raw, err := callHost(pluginabi.MethodHostHTTPDo, rpcHostHTTPRequest{HostCallbackID: callbackID, Request: &httpRequest{
		Method: http.MethodPost, URL: config.BaseURL + madAPIEndpoint(request), Headers: madAPIHeaders(request.Headers), Body: madAPIPayload(request),
	}})
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	var response pluginapi.HTTPResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	if response.StatusCode >= http.StatusBadRequest {
		return pluginapi.HTTPResponse{}, fmt.Errorf("MadAPI returned HTTP %d", response.StatusCode)
	}
	return response, nil
}

func forwardMadAPIStream(ctx context.Context, request rpcExecutorRequest) error {
	config := loadedConfig()
	if !config.Enabled || config.BaseURL == "" {
		return fmt.Errorf("dynamic MadAPI executor is not configured")
	}
	maxRetries := config.BootstrapRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	for attempt := 0; ; attempt++ {
		err := forwardMadAPIStreamAttempt(ctx, request, config)
		if err == nil || !errors.Is(err, errMadAPIEmptyStream) || attempt >= maxRetries {
			return err
		}
	}
}

func forwardMadAPIStreamAttempt(ctx context.Context, request rpcExecutorRequest, config pluginConfig) error {
	raw, err := callHost(pluginabi.MethodHostHTTPDoStream, rpcHostHTTPRequest{HostCallbackID: request.HostCallbackID, Request: &httpRequest{
		Method: http.MethodPost, URL: config.BaseURL + madAPIEndpoint(request.ExecutorRequest), Headers: madAPIHeaders(request.Headers), Body: madAPIPayload(request.ExecutorRequest),
	}})
	if err != nil {
		return err
	}
	var response hostStreamResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return err
	}
	if strings.TrimSpace(response.StreamID) == "" {
		return fmt.Errorf("MadAPI stream did not return a stream id")
	}
	defer func() {
		_, _ = callHost(pluginabi.MethodHostHTTPStreamClose, rpcHostHTTPStreamCloseRequest{StreamID: response.StreamID})
	}()
	seenTerminalMarker := false
	seenUpstreamEvent := false
	pendingLine := make([]byte, 0, 4096)
	emitLine := func(line []byte) error {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			return nil
		}
		if isValidSSEDataLine(line) {
			seenUpstreamEvent = true
		}
		if bytes.Contains(line, []byte("[DONE]")) {
			seenTerminalMarker = true
		}
		payload := make([]byte, len(line)+1)
		copy(payload, line)
		payload[len(line)] = '\n'
		_, err := callHost(pluginabi.MethodHostStreamEmit, rpcStreamEmitRequest{StreamID: request.StreamID, Payload: payload})
		return err
	}
	emitPayload := func(payload []byte, final bool) error {
		pendingLine = append(pendingLine, payload...)
		for {
			newline := bytes.IndexByte(pendingLine, '\n')
			if newline < 0 {
				break
			}
			if err := emitLine(pendingLine[:newline]); err != nil {
				return err
			}
			pendingLine = pendingLine[newline+1:]
		}
		if final && len(pendingLine) > 0 {
			if err := emitLine(pendingLine); err != nil {
				return err
			}
			pendingLine = pendingLine[:0]
		}
		return nil
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		chunkRaw, err := callHost(pluginabi.MethodHostHTTPStreamRead, rpcHostHTTPStreamReadRequest{StreamID: response.StreamID})
		if err != nil {
			return err
		}
		var chunk hostStreamChunk
		if err := json.Unmarshal(chunkRaw, &chunk); err != nil {
			return err
		}
		if chunk.Error != "" {
			return fmt.Errorf("MadAPI stream error: %s", chunk.Error)
		}
		if len(chunk.Payload) > 0 {
			if err := emitPayload(chunk.Payload, false); err != nil {
				return err
			}
		}
		if chunk.Done {
			if err := emitPayload(nil, true); err != nil {
				return err
			}
			break
		}
	}
	if !seenUpstreamEvent {
		return errMadAPIEmptyStream
	}
	// MadAPI can close a valid OpenAI-compatible SSE stream without a literal
	// terminal marker. CPA's Responses translator needs that marker to emit the
	// final response.completed event, just as its built-in compatibility executor does.
	if !seenTerminalMarker {
		if _, err := callHost(pluginabi.MethodHostStreamEmit, rpcStreamEmitRequest{StreamID: request.StreamID, Payload: []byte("data: [DONE]\n\n")}); err != nil {
			return err
		}
	}
	if response.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("MadAPI returned HTTP %d", response.StatusCode)
	}
	return nil
}

func isValidSSEDataLine(line []byte) bool {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return false
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	return len(payload) > 0 && !bytes.Equal(payload, []byte("[DONE]"))
}

func madAPIHeaders(headers http.Header) http.Header {
	authorization := strings.TrimSpace(headers.Get("Authorization"))
	if authorization == "" {
		return http.Header{"Content-Type": []string{"application/json"}}
	}
	return http.Header{"Authorization": []string{authorization}, "Content-Type": []string{"application/json"}}
}

func madAPIEndpoint(request pluginapi.ExecutorRequest) string {
	switch strings.ToLower(strings.TrimSpace(request.Format)) {
	case openAIImageFormat:
		return imagesGenerationsPath
	case openAIResponsesFormat:
		return responsesPath
	default:
		return chatCompletionsPath
	}
}

func madAPIPayload(request pluginapi.ExecutorRequest) []byte {
	payload := bytes.Clone(request.Payload)
	if strings.EqualFold(strings.TrimSpace(request.Format), openAIResponsesFormat) {
		payload = ensureNativeImageGenerationTool(payload)
	}
	if !strings.EqualFold(strings.TrimSpace(headerValue(request.Headers, cockpitHeader)), "1") {
		return payload
	}
	model := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
	upstream, ok := cockpitModelAliases[model]
	if !ok || upstream == model {
		return payload
	}
	updated, err := sjson.SetBytes(payload, "model", upstream)
	if err != nil {
		return payload
	}
	return updated
}

func ensureNativeImageGenerationTool(payload []byte) []byte {
	if !json.Valid(payload) {
		return payload
	}
	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		for _, tool := range tools.Array() {
			if strings.EqualFold(strings.TrimSpace(tool.Get("type").String()), "image_generation") {
				return payload
			}
		}
	}
	tool := []byte(`{"type":"image_generation","action":"generate","model":"gpt-image-2"}`)
	var (
		updated []byte
		err     error
	)
	if tools.IsArray() {
		updated, err = sjson.SetRawBytes(payload, "tools.-1", tool)
	} else {
		updated, err = sjson.SetRawBytes(payload, "tools", append([]byte{'['}, append(tool, ']')...))
	}
	if err != nil {
		return payload
	}
	return updated
}

func headerValue(headers http.Header, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	request := (*C.uint8_t)(C.CBytes(rawPayload))
	defer C.free(unsafe.Pointer(request))
	var response C.cliproxy_buffer
	code := C.call_host_api(cMethod, request, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response (code=%d)", method, int(code))
	}
	var result envelope
	if err := json.Unmarshal(rawResponse, &result); err != nil {
		return nil, err
	}
	if !result.OK {
		if result.Error != nil {
			return nil, fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if code != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, int(code))
	}
	return result.Result, nil
}

func okEnvelope(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func closePluginStream(streamID, errMessage string) {
	if strings.TrimSpace(streamID) == "" {
		return
	}
	_, _ = callHost(pluginabi.MethodHostStreamClose, rpcStreamCloseRequest{StreamID: streamID, Error: strings.TrimSpace(errMessage)})
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
