package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestBootstrapKeepsOfficialImageGenerationEnabled(t *testing.T) {
	config := bootstrapConfig(18317, t.TempDir())
	if !strings.Contains(config, "disable-image-generation: false") {
		t.Fatalf("official image generation default is not enabled: %s", config)
	}
}

func TestSynchronousDispatchNeverCallsLegacySettlement(t *testing.T) {
	var settlements atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/settle" {
			settlements.Add(1)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	dispatch, executeID, err := prepareExecuteDispatch(executeMeta{
		Provider: "openai-compatibility", ChannelID: 3, UserID: 7,
		BaseURL: "https://example.com/v1", APIKey: "upstream-key",
		Model: "gpt-5.6-terra", OriginalModel: "gpt-5.6-terra",
		RequestPath: "/v1/responses",
	})
	if err != nil {
		t.Fatal(err)
	}
	coord := &coordinator{
		control:  &controlClient{baseURL: server.URL, token: strings.Repeat("x", 32), http: server.Client()},
		prepared: make(map[string]*preparedDispatch), preloaded: map[string]*preparedDispatch{executeID: dispatch},
		requests: make(map[string]requestTicket), finalized: make(map[string]struct{}),
	}
	headers := make(http.Header)
	headers.Set(executeIDHeader, executeID)
	intercept := coord.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "sync-request", Headers: headers,
		Body: []byte(`{"model":"shell","input":"hello"}`), Metadata: map[string]any{"request_path": "/v1/responses"},
	})
	if intercept.Terminate {
		t.Fatalf("synchronous dispatch terminated: %s", intercept.ResponseBody)
	}
	coord.HandleUsage(context.Background(), cliproxyusage.Record{APIKey: executeID, Detail: cliproxyusage.Detail{InputTokens: 4, OutputTokens: 2, TotalTokens: 6}})
	coord.CompleteRequest(context.Background(), pluginapi.RequestCompletion{RequestID: "sync-request", Outcome: pluginapi.RequestCompletionSucceeded})
	usage, err := coord.waitSynchronousUsage(context.Background(), dispatch, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	coord.cleanupSynchronousByPointer(dispatch)
	if settlements.Load() != 0 {
		t.Fatalf("legacy settlements = %d, want 0", settlements.Load())
	}
	if usage.InputTokens != 4 || usage.OutputTokens != 2 {
		t.Fatalf("synchronous usage = %+v", usage)
	}
	if _, ok := coord.prepared["sync-request"]; ok {
		t.Fatal("synchronous request was not cleaned up")
	}
}

func TestCoordinatorMatchesConcurrentUsageByTicket(t *testing.T) {
	settled := make(map[string]int64)
	var settledMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request settleRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		settledMu.Lock()
		settled[request.Ticket] = request.Usage.InputTokens
		settledMu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	coord := &coordinator{
		control: &controlClient{baseURL: server.URL, token: strings.Repeat("x", 32), http: server.Client()},
		prepared: map[string]*preparedDispatch{
			"request-a": {ticket: "ticket-a"},
			"request-b": {ticket: "ticket-b"},
		},
		requests: map[string]requestTicket{
			"request-a": {ticket: "ticket-a", authID: "shared-auth"},
			"request-b": {ticket: "ticket-b", authID: "shared-auth"},
		},
		finalized: make(map[string]struct{}),
	}

	coord.CompleteRequest(context.Background(), pluginapi.RequestCompletion{RequestID: "request-b", Outcome: pluginapi.RequestCompletionSucceeded})
	coord.HandleUsage(context.Background(), cliproxyusage.Record{AuthID: "shared-auth", APIKey: "ticket-b", Detail: cliproxyusage.Detail{InputTokens: 20, TotalTokens: 20}})
	coord.HandleUsage(context.Background(), cliproxyusage.Record{AuthID: "shared-auth", APIKey: "ticket-a", Detail: cliproxyusage.Detail{InputTokens: 10, TotalTokens: 10}})
	coord.CompleteRequest(context.Background(), pluginapi.RequestCompletion{RequestID: "request-a", Outcome: pluginapi.RequestCompletionSucceeded})

	settledMu.Lock()
	defer settledMu.Unlock()
	if len(settled) != 2 || settled["ticket-a"] != 10 || settled["ticket-b"] != 20 {
		t.Fatalf("settlements = %#v", settled)
	}
}

func TestCoordinatorUsesOfficialDispatchAndSettlesOnce(t *testing.T) {
	var settlements atomic.Int32
	var settledInputTokens atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth":
			if r.Header.Get("Authorization") != "Bearer user-key" {
				http.Error(w, "missing user key", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"principal":"user:1:token:2"}`))
		case "/dispatch":
			_, _ = w.Write([]byte(`{"ticket":"ticket-1","model":"gpt-5.6-terra","provider":"openai-compatibility","auth_index":"3","auth":{"id":"auth-stable","provider":"openai-compatibility","status":"active","attributes":{"api_key":"upstream","base_url":"https://example.com/v1"}}}`))
		case "/settle":
			var request settleRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			settledInputTokens.Store(request.Usage.InputTokens)
			settlements.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &controlClient{baseURL: server.URL, token: "01234567890123456789012345678901", http: server.Client()}
	coord := &coordinator{
		control:   client,
		prepared:  make(map[string]*preparedDispatch),
		requests:  make(map[string]requestTicket),
		finalized: make(map[string]struct{}),
	}
	headers := http.Header{"Authorization": []string{"Bearer user-key"}}
	intercept := coord.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "request-1", RequestedModel: "gpt-5.6-terra", Headers: headers,
		Body:     []byte(`{"model":"gpt-5.6-terra","input":"hello"}`),
		Metadata: map[string]any{"request_path": "/v1/responses"},
	})
	if intercept.Terminate {
		t.Fatalf("dispatch terminated: %s", intercept.ResponseBody)
	}
	dispatchID := intercept.Headers.Get(dispatchIDHeader)
	if dispatchID != "request-1" {
		t.Fatalf("dispatch id = %q", dispatchID)
	}
	raw, err := coord.RPopAuth(context.Background(), "gpt-5.6-terra", "session-1", intercept.Headers, 1)
	if err != nil {
		t.Fatal(err)
	}
	var home map[string]any
	if err = json.Unmarshal(raw, &home); err != nil || home["ticket"] != "ticket-1" {
		t.Fatalf("home dispatch = %s, err=%v", raw, err)
	}
	secondRaw, err := coord.RPopAuth(context.Background(), "gpt-5.6-terra", "session-1", intercept.Headers, 1)
	if err != nil || string(secondRaw) != string(raw) {
		t.Fatalf("repeated Home dispatch read = %s, err=%v", secondRaw, err)
	}

	coord.HandleUsage(context.Background(), cliproxyusage.Record{
		AuthID: "auth-stable", APIKey: "ticket-1",
		Detail: cliproxyusage.Detail{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
	})
	coord.CompleteRequest(context.Background(), pluginapi.RequestCompletion{RequestID: "request-1", Outcome: pluginapi.RequestCompletionSucceeded})
	if settlements.Load() != 1 {
		t.Fatalf("settlements = %d, want exactly 1", settlements.Load())
	}
	if settledInputTokens.Load() != 10 {
		t.Fatalf("settled input tokens = %d, want 10", settledInputTokens.Load())
	}
	if _, exists := coord.prepared["request-1"]; exists {
		t.Fatal("prepared dispatch was not released after settlement")
	}
}

func TestAccessProviderDelegatesAuthenticationToNewAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer valid" {
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"principal":"user:9:token:8"}`))
	}))
	defer server.Close()
	provider := &newAPIAccessProvider{control: &controlClient{baseURL: server.URL, token: "01234567890123456789012345678901", http: server.Client()}}
	request := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	request.Header.Set("Authorization", "Bearer valid")
	result, authErr := provider.Authenticate(context.Background(), request)
	if authErr != nil || result == nil || result.Principal != "user:9:token:8" {
		t.Fatalf("result=%+v authErr=%v", result, authErr)
	}
}
