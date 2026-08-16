package relayconvert

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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

func TestInternalCodexChatConversionDoesNotChangeExternalV1Contract(t *testing.T) {
	request := dto.OpenAIResponsesRequest{
		Model: "deepseek-v4-flash",
		Input: json.RawMessage(`[
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"checking"}]},
			{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"},
			{"type":"additional_tools","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}
		]`),
		Text:  json.RawMessage(`{"format":{"type":"json_object"}}`),
		Tools: json.RawMessage(`[{"type":"function","name":"lookup","parameters":{"type":"object"}}]`),
	}

	external, err := ConvertCodexResponsesRequestToChatRequest(request)
	require.NoError(t, err)
	internal, err := ConvertInternalCodexResponsesRequestToChatRequest(request)
	require.NoError(t, err)
	externalJSON, err := json.Marshal(external)
	require.NoError(t, err)
	internalJSON, err := json.Marshal(internal)
	require.NoError(t, err)

	require.Equal(t, int64(3), gjson.GetBytes(externalJSON, "messages.#").Int())
	require.False(t, gjson.GetBytes(externalJSON, "response_format").Exists())
	require.Equal(t, int64(2), gjson.GetBytes(externalJSON, "tools.#").Int())

	require.Equal(t, int64(2), gjson.GetBytes(internalJSON, "messages.#").Int())
	require.Equal(t, "json_object", gjson.GetBytes(internalJSON, "response_format.type").String())
	require.Equal(t, int64(1), gjson.GetBytes(internalJSON, "tools.#").Int())
}
