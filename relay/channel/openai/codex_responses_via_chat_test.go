package openai

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
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
