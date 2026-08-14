package relayconvert

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func TestConvertCodexResponsesRequestToChatPreservesCodexFeatures(t *testing.T) {
	stream := true
	request := dto.OpenAIResponsesRequest{
		Model:      "kimi-k3",
		Input:      json.RawMessage(`[{"role":"user","content":"search"}]`),
		Stream:     &stream,
		Text:       json.RawMessage(`{"verbosity":"high"}`),
		Tools:      json.RawMessage(`[{"type":"web_search","search_context_size":"high"},{"type":"namespace","name":"mcp__demo","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}]`),
		ToolChoice: json.RawMessage(`"auto"`),
	}

	converted, err := ConvertCodexResponsesRequestToChatRequest(request)

	require.NoError(t, err)
	require.Equal(t, "kimi-k3", converted.Model)
	require.NotNil(t, converted.Stream)
	require.True(t, *converted.Stream)
	require.NotNil(t, converted.WebSearchOptions)
	require.Equal(t, "high", converted.WebSearchOptions.SearchContextSize)
	require.JSONEq(t, `"high"`, string(converted.Verbosity))
	require.Len(t, converted.Tools, 1)
	require.Equal(t, "mcp__demo__lookup", converted.Tools[0].Function.Name)
}
