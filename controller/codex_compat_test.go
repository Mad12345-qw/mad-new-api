package controller

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func withCodexCompatibilityTestServer(t *testing.T, handler http.Handler, textModels ...string) string {
	t.Helper()
	server := httptest.NewServer(handler)
	originalBaseURL := codexInternalBaseURL
	originalPricingProvider := codexTextPricingProvider
	originalRelayExecutor := codexRelayExecutor
	codexInternalBaseURL = server.URL
	codexTextPricingProvider = func() map[string]struct{} {
		models := make(map[string]struct{}, len(textModels))
		for _, modelName := range textModels {
			models[modelName] = struct{}{}
		}
		return models
	}
	codexRelayExecutor = func(c *gin.Context, _ types.RelayFormat) {
		originalBody, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		var request dto.OpenAIResponsesRequest
		require.NoError(t, json.Unmarshal(originalBody, &request))
		if value, ok := common.GetContextKey(c, constant.ContextKeyRelayRequestPreprocessor); ok {
			if err := value.(func(dto.Request) error)(&request); err != nil {
				codexCompatibilityError(c, http.StatusBadRequest, err)
				return
			}
		}
		restore, err := relayconvert.PrepareCodexResponsesRequest(&request)
		require.NoError(t, err)
		if installerValue, ok := c.Get(relayconvert.CodexResponsesRestoreInstallerContextKey); ok {
			installerValue.(relayconvert.CodexResponsesRestoreInstaller)(restore)
		}
		upstreamBody, err := json.Marshal(request)
		require.NoError(t, err)
		upstreamRequest, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, server.URL+"/responses", bytes.NewReader(upstreamBody))
		require.NoError(t, err)
		upstreamRequest.Header.Set("Authorization", c.GetHeader("Authorization"))
		upstreamRequest.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(upstreamRequest)
		require.NoError(t, err)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, readErr := io.ReadAll(resp.Body)
			require.NoError(t, readErr)
			c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
			return
		}
		if request.Stream == nil || !*request.Stream {
			body, readErr := io.ReadAll(resp.Body)
			require.NoError(t, readErr)
			service.IOCopyBytesGracefully(c, resp, body)
			return
		}
		scanner := bufio.NewScanner(resp.Body)
		eventType := ""
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event:") {
				eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				continue
			}
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			var event dto.ResponsesStreamResponse
			require.NoError(t, json.Unmarshal([]byte(payload), &event))
			if event.Type == "" {
				event.Type = eventType
			}
			require.NoError(t, helper.ResponseChunkData(c, event, payload))
		}
		require.NoError(t, scanner.Err())
	}
	t.Cleanup(func() {
		server.Close()
		codexInternalBaseURL = originalBaseURL
		codexTextPricingProvider = originalPricingProvider
		codexRelayExecutor = originalRelayExecutor
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
		"grok-4.6", "kimi-k3", "qwen3.8-max-preview", "gpt-5.6-sol-pro", "gpt-5.6-terra-pro",
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
		require.Equal(t, true, entry["supports_search_tool"])
	}
	require.Equal(t, "gpt-5.5", clientModelByUpstream["claude-fable-5"])
	require.Equal(t, "gpt-5.4", clientModelByUpstream["claude-opus-5"])
	require.Equal(t, "gpt-5.6-sol", clientModelByUpstream["gpt-5.6-sol"])
	require.Equal(t, "gpt-5.6-terra", clientModelByUpstream["gpt-5.6-terra"])
	require.Equal(t, "gpt-5.6-luna", clientModelByUpstream["gpt-5.6-luna"])
	require.Equal(t, "gpt-5.4-mini", clientModelByUpstream["grok-4.6"])
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
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-5.6-sol","object":"model","supported_endpoint_types":["openai-response"]},{"id":"grok-4.6","object":"model","supported_endpoint_types":["openai-response"]},{"id":"future-model","object":"model","supported_endpoint_types":["openai-response"]}]}`)
	}), "gpt-5.6-sol", "grok-4.6", "future-model")

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

func TestCodexCockpitGrokShellRewritesToGrok46AndPreservesNativeSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	raw := []byte(`{"model":"gpt-5.4-mini","input":"latest facts","tools":[{"type":"web_search","search_context_size":"high"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/cockpit/v1/responses", bytes.NewReader(raw))

	rewritten, err := rewriteCodexCockpitRequestBody(c, raw)

	require.NoError(t, err)
	require.Equal(t, "grok-4.6", gjson.GetBytes(rewritten, "model").String())
	require.Equal(t, "web_search", gjson.GetBytes(rewritten, "tools.0.type").String())
	require.Equal(t, "high", gjson.GetBytes(rewritten, "tools.0.search_context_size").String())
	require.Equal(t, int64(1), gjson.GetBytes(rewritten, "tools.#").Int())
	require.True(t, gjson.GetBytes(rewritten, "stream").Bool())
}

func TestCodexCockpitAddsHostedSearchForNativeProviderAdapter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	raw := []byte(`{"model":"gpt-5.4","input":"latest facts","tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/cockpit/v1/responses", bytes.NewReader(raw))

	rewritten, err := rewriteCodexCockpitRequestBody(c, raw)

	require.NoError(t, err)
	require.Equal(t, "claude-opus-5", gjson.GetBytes(rewritten, "model").String())
	require.Equal(t, "function", gjson.GetBytes(rewritten, "tools.0.type").String())
	require.Equal(t, "web_search", gjson.GetBytes(rewritten, "tools.1.type").String())
}

func TestCodexCockpitRespectsExplicitSearchDisable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	raw := []byte(`{"model":"gpt-5.6-terra","input":"offline only","tool_choice":"none","stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/cockpit/v1/responses", bytes.NewReader(raw))

	rewritten, err := rewriteCodexCockpitRequestBody(c, raw)

	require.NoError(t, err)
	require.False(t, gjson.GetBytes(rewritten, "tools").Exists())
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
	require.Equal(t, true, model["support_verbosity"])
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

func TestBuildCodexModelAdvertisesOnlyImplementedNativeSearch(t *testing.T) {
	for _, modelName := range []string{
		"claude-fable-5", "claude-haiku-4-5", "claude-opus-4-8", "claude-opus-5", "claude-sonnet-5",
		"deepseek-v4-flash", "deepseek-v4-pro", "gemini-3.6-flash", "glm-5-2", "glm-5.2", "glm-5.3",
		"gpt-5.5", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-sol-pro", "gpt-5.6-terra", "gpt-5.6-terra-pro",
		"grok-4.5", "grok-4.6", "kimi-k3", "qwen3.8-max-preview",
	} {
		model := buildCodexModel(dto.OpenAIModels{Id: modelName}, 0)
		require.Equal(t, true, model["supports_search_tool"], modelName)
	}
	for _, modelName := range []string{"future-text-model"} {
		model := buildCodexModel(dto.OpenAIModels{Id: modelName}, 0)
		require.Equal(t, false, model["supports_search_tool"], modelName)
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
		require.Contains(t, tools, `"name":"mcp__github__get_me"`)
		require.Contains(t, tools, `"name":"terminal__exec"`)
		require.NotContains(t, tools, `"type":"namespace"`)
		require.JSONEq(t, `true`, string(request.ParallelToolCalls))
		require.NotContains(t, string(request.Input), "additional_tools")

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

func TestCodexResponsesPreservesStatefulInputOnNativeRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	withCodexCompatibilityTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/responses", r.URL.Path)
		var request dto.OpenAIResponsesRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "resp_1", request.PreviousResponseID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-stateful","object":"response","status":"completed","model":"gpt-test","output":[]}`)
	}), "gpt-test")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hello","previous_response_id":"resp_1","stream":false}`))
	c.Request.Header.Set("Authorization", "Bearer test-token")
	c.Request.Header.Set("Content-Type", "application/json")

	CodexResponses(c)

	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, http.StatusOK, recorder.Code)
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

func TestCodexInternalResponseHeaderTimeoutIsLongEnoughForSlowTextModels(t *testing.T) {
	transport, ok := codexInternalHTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	require.Equal(t, 360*time.Second, transport.ResponseHeaderTimeout)
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

func TestCodexResponsesSingleLayerPreparesAndRestoresWithoutLoopback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalExecutor := codexRelayExecutor
	originalPricingProvider := codexTextPricingProvider
	codexTextPricingProvider = func() map[string]struct{} { return map[string]struct{}{"gpt-5.6-sol": {}} }
	defer func() {
		codexRelayExecutor = originalExecutor
		codexTextPricingProvider = originalPricingProvider
	}()

	codexRelayExecutor = func(c *gin.Context, format types.RelayFormat) {
		require.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), format)
		require.Equal(t, "/v1/responses", c.Request.URL.Path)
		value, ok := common.GetContextKey(c, constant.ContextKeyRelayRequestPreprocessor)
		require.True(t, ok)
		preprocess, ok := value.(func(dto.Request) error)
		require.True(t, ok)
		stream := true
		request := &dto.OpenAIResponsesRequest{
			Model:  "gpt-5.6-sol",
			Stream: &stream,
			Tools:  json.RawMessage(`[{"type":"namespace","name":"mcp","tools":[{"type":"custom","name":"apply_patch"}]}]`),
			Input:  json.RawMessage(`[{"type":"additional_tools","tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]`),
		}
		restore, err := relayconvert.PrepareCodexResponsesRequest(request)
		require.NoError(t, err)
		installerValue, ok := c.Get(relayconvert.CodexResponsesRestoreInstallerContextKey)
		require.True(t, ok)
		installerValue.(relayconvert.CodexResponsesRestoreInstaller)(restore)
		require.NoError(t, preprocess(request))
		require.Equal(t, "mcp__apply_patch", gjson.GetBytes(request.Tools, "0.name").String())
		require.Equal(t, "shell", gjson.GetBytes(request.Tools, "1.name").String())
		require.Equal(t, "message", gjson.GetBytes(request.Input, "0.type").String())

		require.NoError(t, helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: "response.output_item.done"}, `{"type":"response.output_item.done","item":{"type":"function_call","name":"mcp__apply_patch","arguments":"{\"input\":\"patch text\"}"}}`))
		require.NoError(t, helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: "response.completed"}, `{"type":"response.completed","response":{"id":"resp-single","status":"completed"}}`))
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","input":"hello","stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	CodexResponses(c)

	body := recorder.Body.String()
	require.Contains(t, body, `"type":"custom_tool_call"`)
	require.Contains(t, body, `"name":"apply_patch"`)
	require.Contains(t, body, `"namespace":"mcp"`)
	require.Equal(t, 1, strings.Count(body, "event: response.completed"))
	require.NotContains(t, body, "response.incomplete")
}

func TestCodexResponsesSingleLayerPreservesEOFTerminalRepair(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalExecutor := codexRelayExecutor
	originalPricingProvider := codexTextPricingProvider
	codexTextPricingProvider = func() map[string]struct{} { return map[string]struct{}{"gpt-5.6-sol": {}} }
	defer func() {
		codexRelayExecutor = originalExecutor
		codexTextPricingProvider = originalPricingProvider
	}()

	codexRelayExecutor = func(c *gin.Context, _ types.RelayFormat) {
		value, _ := common.GetContextKey(c, constant.ContextKeyRelayRequestPreprocessor)
		preprocess := value.(func(dto.Request) error)
		stream := true
		request := &dto.OpenAIResponsesRequest{Model: "gpt-5.6-sol", Input: json.RawMessage(`"hello"`), Stream: &stream}
		restore, err := relayconvert.PrepareCodexResponsesRequest(request)
		require.NoError(t, err)
		installerValue, ok := c.Get(relayconvert.CodexResponsesRestoreInstallerContextKey)
		require.True(t, ok)
		installerValue.(relayconvert.CodexResponsesRestoreInstaller)(restore)
		require.NoError(t, preprocess(request))
		require.NoError(t, helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: "response.created"}, `{"type":"response.created","response":{"id":"resp-eof","status":"in_progress"}}`))
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","input":"hello","stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	CodexResponses(c)

	body := recorder.Body.String()
	require.Equal(t, 1, strings.Count(body, "event: response.incomplete"))
	require.Contains(t, body, `"id":"resp-eof"`)
	require.Contains(t, body, `"code":"stream_ended"`)
}
