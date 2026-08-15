package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexResponsesWebsocketUsesExistingResponsesRouteForEveryTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	var secondInputCount atomic.Int32
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/responses", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		require.Equal(t, relayconvert.CodexResponsesInternalMarker(), r.Header.Get("X-MadAPI-Codex-Compat"))
		var request struct {
			Model  string            `json:"model"`
			Input  []json.RawMessage `json:"input"`
			Stream bool              `json:"stream"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "gpt-test", request.Model)
		require.True(t, request.Stream)
		if call == 2 {
			secondInputCount.Store(int32(len(request.Input)))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-%d\",\"status\":\"in_progress\"}}\n\n", call)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"id\":\"assistant-%d\",\"content\":[]}}\n\n", call)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-%d\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"id\":\"assistant-%d\",\"content\":[]}]}}\n\n", call, call)
	}), "gpt-test")

	router := gin.New()
	router.GET("/codex/v1/responses", func(c *gin.Context) {
		c.Set("token_id", 123)
		CodexResponsesWebsocket(c)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/codex/v1/responses"
	header := make(http.Header)
	header.Set("Authorization", "Bearer test-token")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	require.NoError(t, err)
	defer conn.Close()

	require.NoError(t, conn.WriteJSON(gin.H{
		"type":  "response.create",
		"model": "gpt-test",
		"input": []any{gin.H{"type": "message", "role": "user", "id": "user-1", "content": "hello"}},
	}))
	readCodexWebsocketUntilCompleted(t, conn)
	require.NoError(t, conn.WriteJSON(gin.H{
		"type":                 "response.append",
		"previous_response_id": "resp-1",
		"input":                []any{gin.H{"type": "message", "role": "user", "id": "user-2", "content": "next"}},
	}))
	readCodexWebsocketUntilCompleted(t, conn)
	require.Equal(t, int32(2), calls.Load())
	require.Equal(t, int32(3), secondInputCount.Load())
}

func TestCodexResponsesWebsocketPrewarmDoesNotCallOrBillInnerRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}), "gpt-test")

	router := gin.New()
	router.GET("/codex/v1/responses", func(c *gin.Context) {
		c.Set("token_id", 456)
		CodexResponsesWebsocket(c)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/codex/v1/responses"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	require.NoError(t, conn.WriteJSON(gin.H{
		"type": "response.create", "model": "gpt-test", "input": []any{}, "generate": false,
	}))
	readCodexWebsocketUntilCompleted(t, conn)
	require.Equal(t, int32(0), calls.Load())
}

func TestCodexResponsesWebsocketDoesNotAddConnectionConcurrencyLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}), "gpt-test")

	router := gin.New()
	router.GET("/codex/v1/responses", func(c *gin.Context) {
		c.Set("token_id", 789)
		CodexResponsesWebsocket(c)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/codex/v1/responses"

	const connections = 72
	opened := make([]*websocket.Conn, 0, connections)
	defer func() {
		for _, conn := range opened {
			_ = conn.Close()
		}
	}()
	for index := 0; index < connections; index++ {
		conn, response, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		require.NoError(t, err, "websocket connection %d", index)
		opened = append(opened, conn)
	}
	for index, conn := range opened {
		require.NoError(t, conn.WriteJSON(gin.H{
			"type": "response.create", "model": "gpt-test", "input": []any{}, "generate": false,
		}), "websocket prewarm %d", index)
		readCodexWebsocketUntilCompleted(t, conn)
	}
}

func TestCodexResponsesDoesNotAddRequestConcurrencyLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const requests = 64
	arrived := make(chan struct{}, requests)
	release := make(chan struct{})
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_concurrent","object":"response","status":"completed","model":"gpt-test","output":[]}`)
	}), "gpt-test")

	recorders := make([]*httptest.ResponseRecorder, requests)
	var workers sync.WaitGroup
	workers.Add(requests)
	for index := 0; index < requests; index++ {
		go func(slot int) {
			defer workers.Done()
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hello","stream":false}`))
			c.Request.Header.Set("Authorization", "Bearer concurrency-test-token")
			c.Request.Header.Set("Content-Type", "application/json")
			CodexResponses(c)
			recorders[slot] = recorder
		}(index)
	}

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for index := 0; index < requests; index++ {
		select {
		case <-arrived:
		case <-timer.C:
			close(release)
			workers.Wait()
			t.Fatalf("only %d of %d concurrent requests reached NewAPI", index, requests)
		}
	}
	close(release)
	workers.Wait()
	for index, recorder := range recorders {
		require.NotNil(t, recorder, "request %d", index)
		require.Equal(t, http.StatusOK, recorder.Code, "request %d: %s", index, recorder.Body.String())
	}
}

func readCodexWebsocketUntilCompleted(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	for {
		_, payload, err := conn.ReadMessage()
		require.NoError(t, err)
		var event struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal(payload, &event))
		require.NotEqual(t, "error", event.Type, string(payload))
		if event.Type == "response.completed" {
			return
		}
	}
}

func withCodexCompatibilityTestServer(t *testing.T, handler http.Handler, textModels ...string) string {
	t.Helper()
	server := httptest.NewServer(handler)
	originalBaseURL := codexInternalBaseURL
	originalPricingProvider := codexTextPricingProvider
	codexInternalBaseURL = server.URL
	codexTextPricingProvider = func() map[string]struct{} {
		models := make(map[string]struct{}, len(textModels))
		for _, modelName := range textModels {
			models[modelName] = struct{}{}
		}
		return models
	}
	t.Cleanup(func() {
		server.Close()
		codexInternalBaseURL = originalBaseURL
		codexTextPricingProvider = originalPricingProvider
	})
	return server.URL
}

func TestCodexListModelsUsesExistingTextPricingAndEndpointMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var authorization string
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		require.Equal(t, "/models", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[`+
			`{"id":"claude-opus-test","object":"model","supported_endpoint_types":["anthropic","openai"]},`+
			`{"id":"media-task-test","object":"model","supported_endpoint_types":["openai"]},`+
			`{"id":"embedding-test","object":"model","supported_endpoint_types":["jina-rerank"]}`+
			`]}`)
	}), "claude-opus-test", "embedding-test")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/codex/v1/models?client_version=1.2.3", nil)
	c.Request.Header.Set("Authorization", "Bearer test-token")

	CodexListModels(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "Bearer test-token", authorization)
	require.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
	var payload codexModelsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Len(t, payload.Models, 1)
	model := payload.Models[0]
	require.Equal(t, "claude-opus-test", model["slug"])
	require.Equal(t, "list", model["visibility"])
	require.Equal(t, false, model["prefer_websockets"])
	require.Equal(t, "medium", model["default_reasoning_level"])
	require.Equal(t, true, model["support_verbosity"])
	require.Equal(t, "low", model["default_verbosity"])
	require.Equal(t, "2911", model["comp_hash"])
	require.Equal(t, "0.124.0", model["minimal_client_version"])
	require.NotEmpty(t, model["supported_reasoning_levels"])
	require.NotEmpty(t, model["model_messages"])
}

func TestCodexListModelsReflectsNewTextModelsWithoutServerRestart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		require.Equal(t, "/models", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"existing-text-model","object":"model","supported_endpoint_types":["openai-response"]}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"existing-text-model","object":"model","supported_endpoint_types":["openai-response"]},{"id":"gpt-5.6-sol-pro","object":"model","supported_endpoint_types":["openai-response"]},{"id":"gpt-5.6-terra-pro","object":"model","supported_endpoint_types":["openai-response"]}]}`)
	}), "existing-text-model", "gpt-5.6-sol-pro", "gpt-5.6-terra-pro")

	wantSlugs := [][]string{
		{"existing-text-model"},
		{"existing-text-model", "gpt-5.6-sol-pro", "gpt-5.6-terra-pro"},
	}
	for index, want := range wantSlugs {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/codex/v1/models?client_version=26.801", nil)
		c.Request.Header.Set("Authorization", "Bearer test-token")

		CodexListModels(c)

		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
		var payload codexModelsResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		slugs := make([]string, 0, len(payload.Models))
		for _, model := range payload.Models {
			slug, _ := model["slug"].(string)
			slugs = append(slugs, slug)
		}
		require.Equal(t, want, slugs, "catalog request %d", index+1)
	}
	require.Equal(t, int32(2), calls.Load())
}

func TestCodexListModelsMakesProModelsExactCapabilityAliases(t *testing.T) {
	gin.SetMode(gin.TestMode)
	modelNames := []string{"gpt-5.6-sol", "gpt-5.6-sol-pro", "gpt-5.6-terra", "gpt-5.6-terra-pro"}
	data := make([]string, 0, len(modelNames))
	for _, modelName := range modelNames {
		data = append(data, fmt.Sprintf(`{"id":%q,"object":"model","owned_by":"openai","supported_endpoint_types":["openai-response"]}`, modelName))
	}
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[`+strings.Join(data, ",")+`]}`)
	}), modelNames...)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/codex/v1/models", nil)
	c.Request.Header.Set("Authorization", "Bearer test-token")
	CodexListModels(c)
	require.Equal(t, http.StatusOK, recorder.Code)

	var payload codexModelsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	bySlug := make(map[string]map[string]any, len(payload.Models))
	for _, entry := range payload.Models {
		bySlug[entry["slug"].(string)] = entry
	}
	for _, pair := range [][2]string{{"gpt-5.6-sol", "gpt-5.6-sol-pro"}, {"gpt-5.6-terra", "gpt-5.6-terra-pro"}} {
		base := bySlug[pair[0]]
		pro := bySlug[pair[1]]
		require.NotNil(t, base)
		require.NotNil(t, pro)
		for _, field := range []string{
			"context_window", "max_context_window", "max_completion_tokens",
			"input_modalities", "supports_image_detail_original", "support_verbosity",
			"default_verbosity", "supported_reasoning_levels", "default_reasoning_level",
			"supports_search_tool", "web_search_tool_type", "apply_patch_tool_type",
		} {
			require.Equal(t, base[field], pro[field], "%s differs for %s", field, pair[1])
		}
	}
}

func TestCodexCockpitListModelsPublishesTheStableAPIKeySelection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	modelNames := []string{
		"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5",
		"claude-fable-5", "claude-opus-4-8", "claude-opus-5", "claude-sonnet-5",
		"deepseek-v4-flash", "deepseek-v4-pro", "gemini-3.6-flash", "glm-5-2",
		"grok-4.5", "kimi-k3", "qwen3.8-max-preview", "gpt-5.6-sol-pro", "gpt-5.6-terra-pro",
	}
	data := make([]string, 0, len(modelNames))
	for _, modelName := range modelNames {
		data = append(data, fmt.Sprintf(`{"id":%q,"object":"model","supported_endpoint_types":["openai-response"]}`, modelName))
	}
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/models", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[`+strings.Join(data, ",")+`]}`)
	}), modelNames...)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/codex/cockpit/v1/models", nil)
	c.Request.Header.Set("Authorization", "Bearer test-token")

	CodexCockpitListModels(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload codexModelsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Len(t, payload.Models, 8)
	clientModelByUpstream := make(map[string]string, len(payload.Models))
	for _, entry := range payload.Models {
		clientModelByUpstream[entry["display_name"].(string)] = entry["slug"].(string)
		require.Equal(t, false, entry["prefer_websockets"])
	}
	require.Equal(t, "gpt-5.5", clientModelByUpstream["claude-fable-5"])
	require.Equal(t, "gpt-5.4", clientModelByUpstream["claude-opus-5"])
	require.Equal(t, "gpt-5.6-sol", clientModelByUpstream["gpt-5.6-sol"])
	require.Equal(t, "gpt-5.6-terra", clientModelByUpstream["gpt-5.6-terra"])
	require.Equal(t, "gpt-5.6-luna", clientModelByUpstream["gpt-5.6-luna"])
	require.Equal(t, "gpt-5.4-mini", clientModelByUpstream["grok-4.5"])
	require.Equal(t, "gpt-5.3-codex", clientModelByUpstream["gpt-5.6-sol-pro"])
	require.Equal(t, "gpt-5.2", clientModelByUpstream["gpt-5.6-terra-pro"])
	_, containsOverflow := clientModelByUpstream["deepseek-v4-pro"]
	require.False(t, containsOverflow)
	_, containsReplacedKimi := clientModelByUpstream["kimi-k3"]
	require.False(t, containsReplacedKimi)
	_, containsReplacedDeepSeek := clientModelByUpstream["deepseek-v4-flash"]
	require.False(t, containsReplacedDeepSeek)
}

func TestCodexCockpitListModelsRefreshesSelectedAvailabilityWithoutServerRestart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-5.6-sol","object":"model","supported_endpoint_types":["openai-response"]}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-5.6-sol","object":"model","supported_endpoint_types":["openai-response"]},{"id":"grok-4.5","object":"model","supported_endpoint_types":["openai-response"]},{"id":"future-model","object":"model","supported_endpoint_types":["openai-response"]}]}`)
	}), "gpt-5.6-sol", "grok-4.5", "future-model")

	wantCounts := []int{1, 2}
	for _, wantCount := range wantCounts {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/codex/cockpit/v1/models", nil)
		c.Request.Header.Set("Authorization", "Bearer test-token")
		CodexCockpitListModels(c)
		require.Equal(t, http.StatusOK, recorder.Code)
		var payload codexModelsResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		require.Len(t, payload.Models, wantCount)
	}
}

func TestCodexCockpitResponsesRewritesOnlyTheIsolatedRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"claude-fable-5","object":"model","supported_endpoint_types":["openai-response"]}]}`)
			return
		}
		require.Equal(t, "/responses", r.URL.Path)
		var request dto.OpenAIResponsesRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "claude-fable-5", request.Model)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-test","object":"response","status":"completed","model":"claude-fable-5","output":[]}`)
	}), "claude-fable-5")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/cockpit/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hello","stream":false}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestCodexCockpitAliasDoesNotChangeEstablishedCodexRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	raw := []byte(`{"model":"gpt-5.6-sol","input":"hello"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", bytes.NewReader(raw))

	rewritten, err := rewriteCodexCockpitRequestBody(c, raw)

	require.NoError(t, err)
	require.Equal(t, raw, rewritten)
}

func TestBuildCodexModelUsesPinnedCodexCapabilityMetadata(t *testing.T) {
	model := buildCodexModel(dto.OpenAIModels{
		Id:      "claude-opus-4-8",
		OwnedBy: "anthropic",
	}, 0)

	require.Equal(t, "Claude Opus 4.8", model["display_name"])
	require.Equal(t, 1000000, model["context_window"])
	require.Equal(t, []string{"text", "image"}, model["input_modalities"])
	require.Equal(t, true, model["supports_image_detail_original"])
	require.Equal(t, false, model["support_verbosity"])
	require.Equal(t, "high", model["default_reasoning_level"])
	levels, ok := model["supported_reasoning_levels"].([]any)
	require.True(t, ok)
	require.Len(t, levels, 5)
	require.Equal(t, "madapi-codex-native-v1", model["comp_hash"])
}

func TestBuildCodexModelUsesMostCompleteCodexEntryForGemini(t *testing.T) {
	model := buildCodexModel(dto.OpenAIModels{Id: "gemini-3.6-flash"}, 0)

	require.Equal(t, 1048576, model["context_window"])
	require.Equal(t, 65536, model["max_completion_tokens"])
	require.Equal(t, []string{"text", "image"}, model["input_modalities"])
	require.Equal(t, "high", model["default_reasoning_level"])
}

func TestBuildCodexModelUsesHighEndDefaultsForUnknownModels(t *testing.T) {
	model := buildCodexModel(dto.OpenAIModels{Id: "future-text-model"}, 0)

	require.Equal(t, 1000000, model["context_window"])
	require.Equal(t, 1000000, model["max_context_window"])
	require.Equal(t, []string{"text"}, model["input_modalities"])
	require.Equal(t, "high", model["default_reasoning_level"])
	require.Equal(t, false, model["support_verbosity"])
	require.Equal(t, "medium", model["default_verbosity"])
	require.Equal(t, false, model["supports_search_tool"])
	require.Equal(t, true, model["prefer_websockets"])
	require.Empty(t, model["service_tiers"])
}

func TestBuildCodexModelUsesOfficialCapabilitiesMissingFromCPA(t *testing.T) {
	deepseek := buildCodexModel(dto.OpenAIModels{Id: "deepseek-v4-flash"}, 0)
	require.Equal(t, 1048576, deepseek["context_window"])
	require.Equal(t, 393216, deepseek["max_completion_tokens"])
	require.Equal(t, "high", deepseek["default_reasoning_level"])

	glm := buildCodexModel(dto.OpenAIModels{Id: "glm-5-2"}, 0)
	require.Equal(t, "GLM-5.2", glm["display_name"])
	require.Equal(t, 1048576, glm["context_window"])
	require.Equal(t, 131072, glm["max_completion_tokens"])
	require.Equal(t, []string{"text"}, glm["input_modalities"])
	require.Equal(t, "high", glm["default_reasoning_level"])

	qwenAlias := buildCodexModel(dto.OpenAIModels{Id: "qwen3.8-max-preview"}, 0)
	require.Equal(t, 1000000, qwenAlias["context_window"])
	require.Equal(t, "high", qwenAlias["default_reasoning_level"])
}

func TestBuildCodexModelDerivesNativeSearchFromProviderFamily(t *testing.T) {
	for _, modelName := range []string{
		"gpt-5.5", "gpt-5.6-luna", "gpt-5.6-terra", "gpt-5.7-future",
		"claude-opus-5", "claude-opus-6-future",
		"grok-4.5", "grok-4.6", "grok-4.7",
	} {
		model := buildCodexModel(dto.OpenAIModels{Id: modelName}, 0)
		require.Equal(t, true, model["supports_search_tool"], modelName)
	}
	for _, modelName := range []string{
		"deepseek-v4-flash", "deepseek-v4-pro", "gemini-3.6-flash", "glm-5-2", "glm-5.3",
		"kimi-k3", "qwen3.8-max-preview", "future-text-model",
	} {
		model := buildCodexModel(dto.OpenAIModels{Id: modelName}, 0)
		require.Equal(t, false, model["supports_search_tool"], modelName)
	}
}

func TestBuildCodexModelDerivesProtocolCapabilitiesForFutureProviderModels(t *testing.T) {
	tests := []struct {
		model     string
		search    bool
		verbosity bool
	}{
		{model: "gpt-5.7", search: true, verbosity: true},
		{model: "claude-opus-6", search: true},
		{model: "grok-4.7", search: true},
		{model: "deepseek-v5", search: false},
		{model: "glm-5.5", search: false},
		{model: "gemini-3.7-flash", search: false},
		{model: "kimi-k4", search: false},
		{model: "qwen4-max", search: false},
	}

	for _, test := range tests {
		model := buildCodexModel(dto.OpenAIModels{Id: test.model}, 0)
		require.Equal(t, test.search, model["supports_search_tool"], test.model)
		require.Equal(t, test.verbosity, model["support_verbosity"], test.model)
	}
}

func TestCodexResponsesDelegatesArbitraryModelsThroughNativeResponsesRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	modelNames := []string{"claude-opus-test", "gemini-flash-test"}
	var calls atomic.Int32
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/responses", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		var request dto.OpenAIResponsesRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Contains(t, modelNames, request.Model)
		require.False(t, *request.Stream)
		require.JSONEq(t, `"Follow the developer instructions"`, string(request.Instructions))
		require.JSONEq(t, `"hello"`, string(request.Input))

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, fmt.Sprintf(`{"id":"resp-test","object":"response","status":"completed","model":%q,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"native response"}]}]}`, request.Model))
	}), modelNames...)

	for _, modelName := range modelNames {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		body := fmt.Sprintf(`{"model":%q,"instructions":"Follow the developer instructions","input":"hello","stream":false}`, modelName)
		c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(body))
		c.Request.Header.Set("Authorization", "Bearer test-token")
		c.Request.Header.Set("Content-Type", "application/json")

		CodexResponses(c)

		require.Equal(t, http.StatusOK, recorder.Code)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		require.Equal(t, "response", payload["object"])
		require.Equal(t, "completed", payload["status"])
	}
	require.Equal(t, int32(len(modelNames)), calls.Load(), "each request must submit exactly one native billable request")
}

func TestCodexResponsesPreservesFunctionToolsForChannelAdaptor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/responses", r.URL.Path)
		var request dto.OpenAIResponsesRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Contains(t, string(request.Tools), `"type":"function"`)
		require.Contains(t, string(request.Tools), `"name":"lookup"`)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-tool","object":"response","status":"completed","model":"gpt-test","output":[]}`)
	}), "gpt-test")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{
		"model":"gpt-test",
		"input":"use the tool",
		"tools":[{"type":"function","name":"lookup","description":"Lookup data","parameters":{"type":"object"}}],
		"stream":false
	}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestCodexResponsesKeepsForcedImageGenerationOnNativeResponsesRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/responses", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "image_generation", payload["tool_choice"].(map[string]any)["type"])
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-image","object":"response","status":"completed","output":[{"id":"ig-1","type":"image_generation_call","status":"completed","result":"aGVsbG8=","output_format":"png"}]}`)
	}), "gpt-5.6-sol")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{
		"model":"gpt-5.6-sol",
		"input":"draw a lighthouse",
		"tools":[{"type":"image_generation","action":"generate","model":"gpt-image-2"}],
		"tool_choice":{"type":"image_generation"},
		"stream":false
	}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, int32(1), calls.Load())
	require.Contains(t, recorder.Body.String(), `"type":"image_generation_call"`)
}

func TestCodexResponsesUsesNativeRouteForResponsesAndNamespaceTools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/responses", r.URL.Path)
		var request dto.OpenAIResponsesRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		tools := string(request.Tools)
		require.Contains(t, tools, `"type":"web_search"`)
		require.Contains(t, tools, `"type":"namespace"`)
		require.Contains(t, tools, `"name":"mcp__github"`)
		require.Contains(t, tools, `"name":"get_me"`)
		require.NotContains(t, tools, `"name":"mcp__github__get_me"`)
		require.JSONEq(t, `true`, string(request.ParallelToolCalls))
		require.Contains(t, string(request.Input), `"type":"additional_tools"`)
		require.Contains(t, string(request.Input), `"type":"namespace"`)
		require.Contains(t, string(request.Input), `"name":"terminal"`)
		require.Contains(t, string(request.Input), `"name":"exec"`)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-tool","object":"response","status":"completed","model":"gpt-test","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_get_me","name":"mcp__github__get_me","arguments":"{}"}]}`)
	}), "gpt-5.5")

	body := `{
		"model":"gpt-5.5",
		"input":[
			{"role":"user","content":"use get_me"},
			{"type":"additional_tools","tools":[{"type":"namespace","name":"terminal","tools":[{"type":"custom","name":"exec"}]}]}
		],
		"tools":[
			{"type":"web_search"},
			{"type":"namespace","name":"mcp__github","tools":[{"type":"function","name":"get_me","parameters":{"type":"object"}}]}
		],
		"tool_choice":"auto",
		"parallel_tool_calls":true,
		"stream":false
	}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(body))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Output []struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Len(t, response.Output, 1)
	require.Equal(t, "get_me", response.Output[0].Name)
	require.Equal(t, "mcp__github", response.Output[0].Namespace)
}

func TestCodexResponsesDoesNotOverrideChannelNativeSearchCapabilities(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/responses", r.URL.Path)
		var request dto.OpenAIResponsesRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Contains(t, string(request.Tools), `"type":"web_search"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-search","object":"response","status":"completed","model":"deepseek-v4-pro","output":[]}`)
	}), "deepseek-v4-pro")

	for _, modelName := range []string{"deepseek-v4-pro"} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		body := fmt.Sprintf(`{"model":%q,"input":"search","tools":[{"type":"web_search"}],"stream":false}`, modelName)
		c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(body))
		c.Request.Header.Set("Authorization", "Bearer test-token")
		c.Request.Header.Set("Content-Type", "application/json")

		CodexResponses(c)

		require.Equal(t, http.StatusOK, recorder.Code, modelName)
	}
	require.Equal(t, int32(1), calls.Load())
}

func TestCodexResponsesForwardsKimiNativeSearchToUnifiedChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/responses", r.URL.Path)
		var request dto.OpenAIResponsesRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "kimi-k3", request.Model)
		require.Contains(t, string(request.Tools), `"type":"web_search"`)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-kimi-search","object":"response","status":"completed","model":"kimi-k3","output":[]}`)
	}), "kimi-k3")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{"model":"kimi-k3","input":"Search current facts","tools":[{"type":"web_search"}],"stream":false}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, int32(1), calls.Load())
}

func TestCodexResponsesPassesNativeResponsesStreamEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/responses", r.URL.Path)
		var request dto.OpenAIResponsesRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.True(t, *request.Stream)

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)
		_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-stream\",\"object\":\"response\",\"status\":\"in_progress\"}}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-stream\",\"object\":\"response\",\"status\":\"completed\"}}\n\n")
		flusher.Flush()
	}), "gpt-test")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hello","stream":true}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	require.Contains(t, body, "response.created")
	require.Contains(t, body, "response.output_text.delta")
	require.Contains(t, body, "response.completed")
}

func TestCodexResponsesRestoresClientToolSearchAndSuppressesFunctionArgumentFrames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"fc_search\",\"type\":\"function_call\",\"call_id\":\"call_search\",\"name\":\"tool_search\",\"arguments\":\"\",\"status\":\"in_progress\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_search\",\"output_index\":0,\"delta\":\"{\\\"query\\\":\\\"github\\\"}\"}\n\n")
		_, _ = fmt.Fprint(w, "event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc_search\",\"output_index\":0,\"arguments\":\"{\\\"query\\\":\\\"github\\\"}\"}\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"fc_search\",\"type\":\"function_call\",\"call_id\":\"call_search\",\"name\":\"tool_search\",\"arguments\":\"{\\\"query\\\":\\\"github\\\"}\",\"status\":\"completed\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_search\",\"status\":\"completed\",\"output\":[{\"id\":\"fc_search\",\"type\":\"function_call\",\"call_id\":\"call_search\",\"name\":\"tool_search\",\"arguments\":\"{\\\"query\\\":\\\"github\\\"}\",\"status\":\"completed\"}]}}\n\n")
	}), "gpt-test")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"find tools","tools":[{"type":"tool_search"}],"stream":true}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	require.Contains(t, body, `"type":"tool_search_call"`)
	require.Contains(t, body, `"execution":"client"`)
	require.NotContains(t, body, "response.function_call_arguments")
	require.NotContains(t, body, `"name":"tool_search"`)
}

func TestCodexResponsesRestoresClaudeHostedSearchWithoutClientFunctionCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"fc_web\",\"type\":\"function_call\",\"call_id\":\"call_web\",\"name\":\"web_search\",\"arguments\":\"\",\"status\":\"in_progress\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_web\",\"output_index\":0,\"delta\":\"{\\\"query\\\":\\\"latest release\\\"}\"}\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"fc_web\",\"type\":\"function_call\",\"call_id\":\"call_web\",\"name\":\"web_search\",\"arguments\":\"{\\\"query\\\":\\\"latest release\\\"}\",\"status\":\"completed\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_web\",\"status\":\"completed\",\"output\":[{\"id\":\"fc_web\",\"type\":\"function_call\",\"call_id\":\"call_web\",\"name\":\"web_search\",\"arguments\":\"{\\\"query\\\":\\\"latest release\\\"}\",\"status\":\"completed\"}]}}\n\n")
	}), "claude-opus-5")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{"model":"claude-opus-5","input":"search","tools":[{"type":"web_search"}],"stream":true}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	require.Contains(t, body, `"type":"web_search_call"`)
	require.Contains(t, body, `"type":"search"`)
	require.Contains(t, body, `"latest release"`)
	require.NotContains(t, body, "response.function_call_arguments")
	require.NotContains(t, body, `"name":"web_search"`)
}

func TestCodexResponsesTurnsPostOutputErrorIntoOneTerminalFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-broken\",\"status\":\"in_progress\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
		_, _ = fmt.Fprint(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"provider disconnected\"}}\n\n")
	}), "gpt-test")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hello","stream":true}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	require.Contains(t, body, "partial")
	require.NotContains(t, body, "event: error")
	require.Equal(t, 1, strings.Count(body, "event: response.failed"))
	require.Contains(t, body, "provider disconnected")
}

func TestCodexResponsesTurnsAbruptStreamEndIntoIncomplete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-short\",\"status\":\"in_progress\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
	}), "gpt-test")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hello","stream":true}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: response.incomplete"))
}

func TestCodexResponsesTurnsExhaustedPreEventFailureIntoTerminalStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"no weighted channel is currently available"}}`)
	}), "gpt-test")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hello","stream":true}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: response.failed"))
	require.Contains(t, recorder.Body.String(), "no weighted channel")
}

func TestCodexResponsesRejectsFixedPriceModelBeforeInnerRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{"model":"video-task-test","input":"hello","stream":false}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponses(c)

	require.Equal(t, int32(0), calls.Load())
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "not a Codex conversation model")
}

func TestCodexResponsesDropsPreviousResponseWithoutInjectingServerHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/responses", r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(body, "previous_response_id").Exists())
		require.Equal(t, "current turn", gjson.GetBytes(body, "input").String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_stateless","object":"response","status":"completed","model":"gpt-test","output":[]}`)
	}), "gpt-test")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"current turn","previous_response_id":"resp_external","stream":false}`))
	c.Request.Header.Set("Authorization", "Bearer stateless-test-token")
	c.Request.Header.Set("Content-Type", "application/json")
	CodexResponses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, int32(1), calls.Load())
}

func TestCodexResponsesPreservesInnerErrorWithoutCompatibilityRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream unavailable","type":"upstream_error"}}`)
	}), "gpt-test")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hello","stream":false}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponses(c)

	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.JSONEq(t, `{"error":{"message":"upstream unavailable","type":"upstream_error"}}`, recorder.Body.String())
}

func TestCodexResponsesCompactUsesTheExistingNewAPIRelay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/responses", r.URL.Path)
		body, readErr := io.ReadAll(r.Body)
		require.NoError(t, readErr)
		var request dto.OpenAIResponsesRequest
		require.NoError(t, json.Unmarshal(body, &request))
		require.Equal(t, "gpt-test", request.Model)
		require.Empty(t, request.PreviousResponseID)
		require.Equal(t, int64(2), gjson.GetBytes(body, "input.#").Int())
		require.Equal(t, "conversation", gjson.GetBytes(body, "input.0.content").String())
		require.Equal(t, "answer", gjson.GetBytes(body, "input.1.content.0.text").String())
		var instructions string
		require.NoError(t, json.Unmarshal(request.Instructions, &instructions))
		require.Contains(t, instructions, codexNativeCompactInstruction)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_compact","object":"response","created_at":123,"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"remember ORCHID"}]}],"usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}}`)
	}), "gpt-test")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses/compact", strings.NewReader(`{"model":"gpt-test","input":[{"role":"user","content":"conversation"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"previous_response_id":"resp_previous"}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponsesCompact(c)

	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "cmp_compact", gjson.GetBytes(recorder.Body.Bytes(), "id").String())
	require.Equal(t, "response.compaction", gjson.GetBytes(recorder.Body.Bytes(), "object").String())
	require.Equal(t, "compaction_summary", gjson.GetBytes(recorder.Body.Bytes(), "output.0.type").String())
	require.Equal(t, "remember ORCHID", gjson.GetBytes(recorder.Body.Bytes(), "output.0.summary").String())
}

func TestCodexResponsesCompactDoesNotAddAnExactUsageGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/responses", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_no_usage","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"summary without usage"}]}]}`)
	}), "gpt-test")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses/compact", strings.NewReader(`{"model":"gpt-test","input":[]}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponsesCompact(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "summary without usage", gjson.GetBytes(recorder.Body.Bytes(), "output.0.summary").String())
	require.Equal(t, "null", gjson.GetBytes(recorder.Body.Bytes(), "usage").Raw)
}

func TestCodexInternalResponseHeaderTimeoutIsLongEnoughForSlowTextModels(t *testing.T) {
	transport, ok := codexInternalHTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	require.Equal(t, 360*time.Second, transport.ResponseHeaderTimeout)
}

func TestCopyCodexTraceHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("X-Oneapi-Request-Id", "req-inner-1")

	copyCodexTraceHeaders(c, resp)

	require.Equal(t, "req-inner-1", recorder.Header().Get("X-Oneapi-Request-Id"))
}

func TestCodexInternalRequestDoesNotPropagateLegacyRoutingHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/cockpit/v1/responses", nil)
	c.Request.Header.Set("X-MadAPI-Codex-Canary", "1")
	c.Request.Header.Set("X-MadAPI-Codex-Cockpit", "1")

	request, err := newCodexInternalRequest(c, http.MethodPost, "/responses", nil)
	require.NoError(t, err)
	require.Empty(t, request.Header.Get("X-MadAPI-Codex-Canary"))
	require.Empty(t, request.Header.Get("X-MadAPI-Codex-Cockpit"))
}
