package deepseek

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestConvertOpenAIResponsesRequestUsesCodexChatAtChannelBoundary(t *testing.T) {
	request := dto.OpenAIResponsesRequest{
		Model: "deepseek-v4-flash",
		Input: json.RawMessage(`[{"role":"user","content":"search"}]`),
		Tools: json.RawMessage(`[{"type":"web_search","search_context_size":"medium"}]`),
	}
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)

	require.NoError(t, err)
	chatRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.NotNil(t, chatRequest.WebSearchOptions)
	require.Equal(t, "medium", chatRequest.WebSearchOptions.SearchContextSize)
}

func TestConvertOpenAIResponsesRequestMapsGLMSearchAfterNativeConversion(t *testing.T) {
	request := dto.OpenAIResponsesRequest{
		Model:      "glm-5.5",
		Input:      json.RawMessage(`"search"`),
		Tools:      json.RawMessage(`[{"type":"web_search"}]`),
		ToolChoice: json.RawMessage(`"auto"`),
	}
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "glm-5.5"},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)

	require.NoError(t, err)
	chatRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Nil(t, chatRequest.WebSearchOptions)
	require.JSONEq(t, `{"enable":true}`, string(chatRequest.WebSearch))
	require.Nil(t, chatRequest.ToolChoice)
}

func TestConvertOpenAIResponsesRequestDowngradesDeepSeekV4ForcedToolChoice(t *testing.T) {
	request := dto.OpenAIResponsesRequest{
		Model:      "deepseek-v4-flash",
		Input:      json.RawMessage(`"call the tool"`),
		Tools:      json.RawMessage(`[{"type":"function","name":"lookup","parameters":{"type":"object"}}]`),
		ToolChoice: json.RawMessage(`"required"`),
	}
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)

	require.NoError(t, err)
	chatRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Equal(t, "auto", chatRequest.ToolChoice)
	require.Len(t, chatRequest.Tools, 1)
}

func TestConvertOpenAIResponsesRequestDoesNotDowngradeGLMOrFutureDeepSeekFamilies(t *testing.T) {
	for _, model := range []string{"glm-5.4", "deepseek-v5-flash"} {
		request := dto.OpenAIResponsesRequest{
			Model:      model,
			Input:      json.RawMessage(`"call the tool"`),
			Tools:      json.RawMessage(`[{"type":"function","name":"lookup","parameters":{"type":"object"}}]`),
			ToolChoice: json.RawMessage(`"required"`),
		}
		info := &relaycommon.RelayInfo{
			RelayFormat: types.RelayFormatOpenAIResponses,
			ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: model},
		}

		converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)

		require.NoError(t, err)
		chatRequest, ok := converted.(*dto.GeneralOpenAIRequest)
		require.True(t, ok)
		require.Equal(t, "required", chatRequest.ToolChoice)
	}
}
