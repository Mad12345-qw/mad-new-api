package relayconvert_test

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/deepseek"
	"github.com/QuantumNous/new-api/relay/channel/gemini"
	"github.com/QuantumNous/new-api/relay/channel/moonshot"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	"github.com/QuantumNous/new-api/relay/channel/xai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexProviderMatrixUsesSelectedProviderContract(t *testing.T) {
	raw := []byte(`{
		"model":"matrix-model",
		"input":"Search current facts and use tools when needed.",
		"tools":[
			{"type":"web_search","search_context_size":"high"},
			{"type":"tool_search"},
			{"type":"custom","name":"apply_patch","format":{"type":"text"}},
			{"type":"namespace","name":"mcp__demo","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}
		],
		"stream":false
	}`)
	boundaryRequest, err := relayconvert.NormalizeCodexResponsesRequest(raw)
	require.NoError(t, err)
	var request dto.OpenAIResponsesRequest
	require.NoError(t, common.Unmarshal(boundaryRequest, &request))
	codexContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	codexContext.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	codexContext.Request.RemoteAddr = "127.0.0.1:12345"
	codexContext.Request.Header.Set("X-MadAPI-Codex-Compat", relayconvert.CodexResponsesInternalMarker())
	selectedRequest := func(t *testing.T, apiType int) dto.OpenAIResponsesRequest {
		t.Helper()
		normalized, normalizeErr := relayconvert.NormalizeCodexResponsesRequestForSelectedProvider(request, apiType)
		require.NoError(t, normalizeErr)
		return normalized
	}

	t.Run("GPT OpenAI Responses keeps native web search", func(t *testing.T) {
		converted, convertErr := (&openai.Adaptor{}).ConvertOpenAIResponsesRequest(codexContext, matrixInfo("gpt-5.7"), selectedRequest(t, appconstant.APITypeOpenAI))
		require.NoError(t, convertErr)
		payload, marshalErr := common.Marshal(converted)
		require.NoError(t, marshalErr)
		assert.Equal(t, "web_search", gjson.GetBytes(payload, "tools.0.type").String())
		assert.Equal(t, "function", gjson.GetBytes(payload, "tools.1.type").String())
		assert.Equal(t, "tool_search", gjson.GetBytes(payload, "tools.1.name").String())
		assert.Equal(t, "function", gjson.GetBytes(payload, "tools.2.type").String())
		assert.Equal(t, "apply_patch", gjson.GetBytes(payload, "tools.2.name").String())
		assert.Equal(t, "function", gjson.GetBytes(payload, "tools.3.type").String())
		assert.Equal(t, "mcp__demo__lookup", gjson.GetBytes(payload, "tools.3.name").String())
	})

	t.Run("Grok xAI Responses keeps native web and x search contract", func(t *testing.T) {
		converted, convertErr := (&xai.Adaptor{}).ConvertOpenAIResponsesRequest(codexContext, matrixInfo("grok-4.7"), selectedRequest(t, appconstant.APITypeXai))
		require.NoError(t, convertErr)
		payload, marshalErr := common.Marshal(converted)
		require.NoError(t, marshalErr)
		assert.Equal(t, "web_search", gjson.GetBytes(payload, "tools.0.type").String())
		assert.Equal(t, "function", gjson.GetBytes(payload, "tools.1.type").String())
		assert.Equal(t, "mcp__demo__lookup", gjson.GetBytes(payload, "tools.1.name").String())
		assert.NotContains(t, string(payload), `"external_web_access"`)
		assert.NotContains(t, string(payload), `"type":"tool_search"`)
		assert.NotContains(t, string(payload), `"name":"apply_patch"`)
	})

	t.Run("Claude Messages keeps Anthropic search and client functions", func(t *testing.T) {
		converted, convertErr := (&claude.Adaptor{}).ConvertOpenAIResponsesRequest(codexContext, matrixInfo("claude-opus-6"), selectedRequest(t, appconstant.APITypeAnthropic))
		require.NoError(t, convertErr)
		payload, marshalErr := common.Marshal(converted)
		require.NoError(t, marshalErr)
		assert.Contains(t, string(payload), `"type":"web_search_20250305"`)
		assert.Contains(t, string(payload), `"name":"tool_search"`)
		assert.Contains(t, string(payload), `"name":"apply_patch"`)
		assert.Contains(t, string(payload), `"name":"mcp__demo__lookup"`)
	})

	t.Run("Gemini keeps Google search and client functions", func(t *testing.T) {
		converted, convertErr := (&gemini.Adaptor{}).ConvertOpenAIResponsesRequest(codexContext, matrixInfo("gemini-3.7-flash"), selectedRequest(t, appconstant.APITypeGemini))
		require.NoError(t, convertErr)
		payload, marshalErr := common.Marshal(converted)
		require.NoError(t, marshalErr)
		assert.Contains(t, string(payload), `"googleSearch":{}`)
		assert.Contains(t, string(payload), `"name":"tool_search"`)
		assert.Contains(t, string(payload), `"name":"apply_patch"`)
		assert.Contains(t, string(payload), `"name":"mcp__demo__lookup"`)
	})

	t.Run("DeepSeek chat keeps search request and client functions", func(t *testing.T) {
		converted, convertErr := (&deepseek.Adaptor{}).ConvertOpenAIResponsesRequest(codexContext, matrixInfo("deepseek-v5"), selectedRequest(t, appconstant.APITypeDeepSeek))
		require.NoError(t, convertErr)
		payload, marshalErr := common.Marshal(converted)
		require.NoError(t, marshalErr)
		assert.Equal(t, "high", gjson.GetBytes(payload, "web_search_options.search_context_size").String())
		assert.Contains(t, string(payload), `"name":"tool_search"`)
		assert.Contains(t, string(payload), `"name":"apply_patch"`)
		assert.Contains(t, string(payload), `"name":"mcp__demo__lookup"`)
	})

	t.Run("GLM chat maps search to its native field and keeps client functions", func(t *testing.T) {
		converted, convertErr := (&deepseek.Adaptor{}).ConvertOpenAIResponsesRequest(codexContext, matrixInfo("glm-5.5"), selectedRequest(t, appconstant.APITypeDeepSeek))
		require.NoError(t, convertErr)
		payload, marshalErr := common.Marshal(converted)
		require.NoError(t, marshalErr)
		assert.JSONEq(t, `{"enable":true}`, gjson.GetBytes(payload, "web_search").Raw)
		assert.False(t, gjson.GetBytes(payload, "web_search_options").Exists())
		assert.Contains(t, string(payload), `"name":"tool_search"`)
		assert.Contains(t, string(payload), `"name":"apply_patch"`)
		assert.Contains(t, string(payload), `"name":"mcp__demo__lookup"`)
	})

	t.Run("Kimi chat keeps search request and client functions", func(t *testing.T) {
		converted, convertErr := (&moonshot.Adaptor{}).ConvertOpenAIResponsesRequest(codexContext, matrixInfo("kimi-k4"), selectedRequest(t, appconstant.APITypeMoonshot))
		require.NoError(t, convertErr)
		payload, marshalErr := common.Marshal(converted)
		require.NoError(t, marshalErr)
		assert.Equal(t, "high", gjson.GetBytes(payload, "web_search_options.search_context_size").String())
		assert.Contains(t, string(payload), `"name":"tool_search"`)
		assert.Contains(t, string(payload), `"name":"apply_patch"`)
		assert.Contains(t, string(payload), `"name":"mcp__demo__lookup"`)
	})
}

func TestCodexProviderMatrixInjectsNativeSearchAfterChannelSelection(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5.6-luna",
		"input":"Search the latest public information.",
		"tools":[
			{"type":"tool_search"},
			{"type":"namespace","name":"mcp__demo","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}
		],
		"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"tool_search"}]},
		"stream":false
	}`)
	boundaryRequest, err := relayconvert.NormalizeCodexResponsesRequest(raw)
	require.NoError(t, err)
	var request dto.OpenAIResponsesRequest
	require.NoError(t, common.Unmarshal(boundaryRequest, &request))

	normalized, err := relayconvert.NormalizeCodexResponsesRequestForSelectedProvider(request, appconstant.APITypeOpenAI)
	require.NoError(t, err)
	codexContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	codexContext.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	codexContext.Request.RemoteAddr = "127.0.0.1:12345"
	codexContext.Request.Header.Set("X-MadAPI-Codex-Compat", relayconvert.CodexResponsesInternalMarker())

	converted, err := (&openai.Adaptor{}).ConvertOpenAIResponsesRequest(codexContext, matrixInfo("gpt-5.6-luna"), normalized)
	require.NoError(t, err)
	payload, err := common.Marshal(converted)
	require.NoError(t, err)
	assert.Equal(t, "web_search", gjson.GetBytes(payload, "tools.0.type").String())
	assert.Equal(t, "function", gjson.GetBytes(payload, "tools.1.type").String())
	assert.Equal(t, "tool_search", gjson.GetBytes(payload, "tools.1.name").String())
	assert.Equal(t, int64(2), gjson.GetBytes(payload, "tools.#").Int())
	assert.Equal(t, "auto", gjson.GetBytes(payload, "tool_choice").String())
}

func TestCodexProviderSwitchConvertsOpenAIClientToolsAndDropsUnsupportedXAIToolSearchHistory(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5.6-luna",
		"previous_response_id":"resp_previous",
		"input":[
			{"type":"custom_tool_call","id":"ctco_01a003bf-75c1-76f0-bc53-46aef06da266","call_id":"call_custom","name":"apply_patch","input":"patch"},
			{"type":"custom_tool_call_output","id":"ctco_01a003bf-75c1-76f0-bc53-46aef06da267","call_id":"call_custom","output":"ok"},
			{"type":"tool_search_call","id":"tsc_search","call_id":"call_search","execution":"client","arguments":{"query":"search"},"status":"completed"}
		],
		"tools":[{"type":"web_search"},{"type":"custom","name":"apply_patch"},{"type":"tool_search"}]
	}`)

	boundaryRequest, err := relayconvert.NormalizeCodexResponsesRequest(raw)
	require.NoError(t, err)
	var request dto.OpenAIResponsesRequest
	require.NoError(t, common.Unmarshal(boundaryRequest, &request))

	openAIRequest, err := relayconvert.NormalizeCodexResponsesRequestForSelectedProvider(request, appconstant.APITypeOpenAI)
	require.NoError(t, err)
	openAIPayload, err := common.Marshal(openAIRequest)
	require.NoError(t, err)
	require.Equal(t, "function_call", gjson.GetBytes(openAIPayload, "input.0.type").String())
	require.Equal(t, "fc_01a003bf-75c1-76f0-bc53-46aef06da266", gjson.GetBytes(openAIPayload, "input.0.id").String())
	require.Equal(t, "function_call_output", gjson.GetBytes(openAIPayload, "input.1.type").String())
	require.Equal(t, "fc_01a003bf-75c1-76f0-bc53-46aef06da267", gjson.GetBytes(openAIPayload, "input.1.id").String())
	require.Equal(t, "function_call", gjson.GetBytes(openAIPayload, "input.2.type").String())
	require.Equal(t, "fc_search", gjson.GetBytes(openAIPayload, "input.2.id").String())
	require.Equal(t, "tool_search", gjson.GetBytes(openAIPayload, "input.2.name").String())

	providerRequest, err := relayconvert.NormalizeCodexResponsesRequestForSelectedProvider(request, appconstant.APITypeXai)
	require.NoError(t, err)
	providerPayload, err := common.Marshal(providerRequest)
	require.NoError(t, err)
	require.Equal(t, "function_call", gjson.GetBytes(providerPayload, "input.0.type").String())
	require.False(t, gjson.GetBytes(providerPayload, "input.0.id").Exists())
	require.Equal(t, "function_call_output", gjson.GetBytes(providerPayload, "input.1.type").String())
	require.False(t, gjson.GetBytes(providerPayload, "input.1.id").Exists())
	require.Equal(t, int64(2), gjson.GetBytes(providerPayload, "input.#").Int())
	require.Equal(t, int64(1), gjson.GetBytes(providerPayload, "tools.#").Int())
	require.Equal(t, "web_search", gjson.GetBytes(providerPayload, "tools.0.type").String())
}

func TestCodexProviderMatrixDoesNotActivateOnExternalV1HeaderSpoof(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.6-flash","input":"search","tools":[{"type":"web_search"},{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`)
	normalized, err := relayconvert.NormalizeCodexResponsesRequest(raw)
	require.NoError(t, err)
	var request dto.OpenAIResponsesRequest
	require.NoError(t, common.Unmarshal(normalized, &request))

	externalContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	externalContext.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	externalContext.Request.RemoteAddr = "127.0.0.1:54321"
	externalContext.Request.Header.Set("X-MadAPI-Codex-Compat", "forged-marker")
	converted, convertErr := (&gemini.Adaptor{}).ConvertOpenAIResponsesRequest(externalContext, matrixInfo("gemini-3.6-flash"), request)
	require.NoError(t, convertErr)
	payload, marshalErr := common.Marshal(converted)
	require.NoError(t, marshalErr)
	assert.NotContains(t, string(payload), `"googleSearch"`)
	assert.Contains(t, string(payload), `"name":"lookup"`)
}

func matrixInfo(model string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		OriginModelName: model,
		RelayFormat:     types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: model,
		},
	}
}
