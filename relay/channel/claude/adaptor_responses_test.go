package claude

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestConvertOpenAIResponsesRequestToClaudePreservesNativeSearch(t *testing.T) {
	request := dto.OpenAIResponsesRequest{
		Model: "claude-opus-5",
		Input: json.RawMessage(`"search"`),
		Tools: json.RawMessage(`[{"type":"web_search","search_context_size":"high"}]`),
	}
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-opus-5"},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)

	require.NoError(t, err)
	claudeRequest, ok := converted.(*dto.ClaudeRequest)
	require.True(t, ok)
	tools, ok := claudeRequest.Tools.([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	search, ok := tools[0].(*dto.ClaudeWebSearchTool)
	require.True(t, ok)
	require.Equal(t, "web_search_20250305", search.Type)
	require.Equal(t, 10, search.MaxUses)
}

func TestClaudeResponsesHandlerReturnsNativeResponse(t *testing.T) {
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`
	c, recorder, resp, info := newClaudeResponsesTestContext(body, false)

	usage, err := ClaudeResponsesHandler(c, resp, info)

	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 5, usage.TotalTokens)
	require.Contains(t, recorder.Body.String(), `"object":"response"`)
	require.Contains(t, recorder.Body.String(), `"text":"hello"`)
}

func TestClaudeResponsesStreamHandlerEmitsTerminalEvent(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	c, recorder, resp, info := newClaudeResponsesTestContext(body, true)

	usage, err := ClaudeResponsesStreamHandler(c, resp, info)

	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 5, usage.TotalTokens)
	output := recorder.Body.String()
	require.Contains(t, output, `event: response.created`)
	require.Contains(t, output, `event: response.output_text.delta`)
	require.Contains(t, output, `"delta":"hello"`)
	require.Equal(t, 1, strings.Count(output, `event: response.completed`))
}

func newClaudeResponsesTestContext(body string, stream bool) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "claude-responses-test")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "claude-opus-5"},
		RelayFormat:        types.RelayFormatOpenAIResponses,
		IsStream:           stream,
		DisablePing:        true,
		ShouldIncludeUsage: true,
	}
	return c, recorder, resp, info
}
