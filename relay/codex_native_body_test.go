package relay

import (
	"encoding/json"
	"testing"

	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/stretchr/testify/require"
)

func nativeCodexTestRequest() *dto.OpenAIResponsesRequest {
	stream := true
	return &dto.OpenAIResponsesRequest{
		Model:  "gpt-5.6-terra",
		Input:  json.RawMessage(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]`),
		Stream: &stream,
		Tools:  json.RawMessage(`[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}]`),
	}
}

func nativeCodexTestRelayInfo(apiType int) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiType:           apiType,
			UpstreamModelName: "gpt-5.6-terra",
		},
	}
}

func TestCanUseCodexNativeResponsesBodyForNativeOpenAI(t *testing.T) {
	original := nativeCodexTestRequest()
	request := cloneResponsesRequestForRelay(original)
	require.True(t, canUseCodexNativeResponsesBody(nativeCodexTestRelayInfo(appconstant.APITypeOpenAI), original, request))
}

func TestCanUseCodexNativeResponsesBodyRejectsNormalizationAndCrossProtocol(t *testing.T) {
	original := nativeCodexTestRequest()
	request := cloneResponsesRequestForRelay(original)
	request.Tools = json.RawMessage(`[{"type":"namespace","name":"mcp","tools":[]}]`)
	require.False(t, canUseCodexNativeResponsesBody(nativeCodexTestRelayInfo(appconstant.APITypeOpenAI), original, request))

	request = cloneResponsesRequestForRelay(original)
	require.False(t, canUseCodexNativeResponsesBody(nativeCodexTestRelayInfo(appconstant.APITypeDeepSeek), original, request))
}

func TestCloneResponsesRequestForRelayCopiesMutablePointers(t *testing.T) {
	original := nativeCodexTestRequest()
	maxTokens := uint(123)
	topLogProbs := 4
	temperature := 0.5
	topP := 0.9
	maxToolCalls := uint(7)
	original.MaxOutputTokens = &maxTokens
	original.TopLogProbs = &topLogProbs
	original.Temperature = &temperature
	original.TopP = &topP
	original.MaxToolCalls = &maxToolCalls
	original.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
	original.Reasoning = &dto.Reasoning{Effort: "high"}

	cloned := cloneResponsesRequestForRelay(original)
	require.NotSame(t, original.MaxOutputTokens, cloned.MaxOutputTokens)
	require.NotSame(t, original.TopLogProbs, cloned.TopLogProbs)
	require.NotSame(t, original.Stream, cloned.Stream)
	require.NotSame(t, original.Temperature, cloned.Temperature)
	require.NotSame(t, original.TopP, cloned.TopP)
	require.NotSame(t, original.MaxToolCalls, cloned.MaxToolCalls)
	require.NotSame(t, original.StreamOptions, cloned.StreamOptions)
	require.NotSame(t, original.Reasoning, cloned.Reasoning)
}
