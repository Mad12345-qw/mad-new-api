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
		Model:      "glm-5-2",
		Input:      json.RawMessage(`"search"`),
		Tools:      json.RawMessage(`[{"type":"web_search"}]`),
		ToolChoice: json.RawMessage(`"auto"`),
	}
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "glm-5-2"},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)

	require.NoError(t, err)
	chatRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Nil(t, chatRequest.WebSearchOptions)
	require.JSONEq(t, `{"enable":true}`, string(chatRequest.WebSearch))
	require.Nil(t, chatRequest.ToolChoice)
}
