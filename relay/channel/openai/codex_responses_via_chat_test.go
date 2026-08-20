package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/stretchr/testify/require"
)

func TestCodexChatToResponsesHandlerReturnsNativeResponseAndUsage(t *testing.T) {
	body := `{"id":"chatcmpl_1","object":"chat.completion","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`
	c, recorder, resp, info := newResponsesChatTestContext(t, body, false)
	info.UpstreamModelName = "kimi-k3"

	usage, err := CodexChatToResponsesHandler(c, info, resp)

	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 5, usage.TotalTokens)
	require.Contains(t, recorder.Body.String(), `"object":"response"`)
	require.Contains(t, recorder.Body.String(), `"text":"hello"`)
}

func TestCodexChatToResponsesStreamHandlerEmitsTerminalEventOnce(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	info.UpstreamModelName = "kimi-k3"

	usage, err := CodexChatToResponsesStreamHandler(c, info, resp)

	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 5, usage.TotalTokens)
	output := recorder.Body.String()
	require.Contains(t, output, `event: response.created`)
	require.Contains(t, output, `event: response.output_text.delta`)
	require.Contains(t, output, `"delta":"hello"`)
	require.Equal(t, 1, strings.Count(output, `event: response.completed`))
}

func TestCodexChatToResponsesStreamHandlerUsesSingleLayerResponseTransformer(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"mcp__apply_patch","arguments":"{}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	info.UpstreamModelName = "kimi-k3"
	request := &dto.OpenAIResponsesRequest{
		Tools: json.RawMessage(`[{"type":"namespace","name":"mcp","tools":[{"type":"custom","name":"apply_patch"}]}]`),
	}
	restore, err := relayconvert.PrepareCodexResponsesRequest(request)
	require.NoError(t, err)
	common.SetContextKey(c, constant.ContextKeyResponsesStreamEventTransformer, func(eventType string, payload []byte) (string, []byte, bool, error) {
		restored, transformErr := restore(payload)
		return eventType, restored, false, transformErr
	})

	usage, relayErr := CodexChatToResponsesStreamHandler(c, info, resp)

	require.Nil(t, relayErr)
	require.NotNil(t, usage)
	output := recorder.Body.String()
	require.Contains(t, output, `"type":"custom_tool_call"`)
	require.Contains(t, output, `"name":"apply_patch"`)
	require.Contains(t, output, `"namespace":"mcp"`)
	require.Equal(t, 1, strings.Count(output, `event: response.completed`))
}
