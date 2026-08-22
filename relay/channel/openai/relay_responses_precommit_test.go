package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"

	"github.com/gin-gonic/gin"
)

func responsesPrecommitContext(t *testing.T, singleLayerCodex bool) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.RemoteAddr = "127.0.0.1:12345"
	previousStreamingTimeout := appconstant.StreamingTimeout
	appconstant.StreamingTimeout = 30
	t.Cleanup(func() {
		appconstant.StreamingTimeout = previousStreamingTimeout
	})
	if singleLayerCodex {
		common.SetContextKey(c, appconstant.ContextKeyRelayRequestPreprocessor, func(dto.Request) error { return nil })
	}
	return c, recorder
}

func responsesPrecommitInfo() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		IsStream: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
		},
	}
}

func responsesHTTPResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestCodexResponsesFirstFailureStaysUncommitted(t *testing.T) {
	c, recorder := responsesPrecommitContext(t, true)
	_, newAPIError := OaiResponsesStreamHandler(c, responsesPrecommitInfo(), responsesHTTPResponse(
		`event: response.failed
data: {"type":"response.failed","response":{"status":"failed","error":{"type":"server_error","code":"upstream_failed","message":"temporary upstream failure"}}}

`,
	))

	require.Error(t, newAPIError)
	require.Equal(t, http.StatusBadGateway, newAPIError.StatusCode)
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())
}

func TestCodexResponsesFirstEmptyStreamStaysUncommitted(t *testing.T) {
	c, recorder := responsesPrecommitContext(t, true)
	_, newAPIError := OaiResponsesStreamHandler(c, responsesPrecommitInfo(), responsesHTTPResponse(""))

	require.Error(t, newAPIError)
	require.Equal(t, http.StatusBadGateway, newAPIError.StatusCode)
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())
}

func TestCodexResponsesClientFailureKeepsBadRequestStatus(t *testing.T) {
	c, _ := responsesPrecommitContext(t, true)
	_, newAPIError := OaiResponsesStreamHandler(c, responsesPrecommitInfo(), responsesHTTPResponse(
		`data: {"type":"response.failed","response":{"status":"failed","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"context window exceeded"}}}

`,
	))

	require.Error(t, newAPIError)
	require.Equal(t, http.StatusBadRequest, newAPIError.StatusCode)
}

func TestCodexResponsesSuccessfulFirstEventReplaysUnchangedStream(t *testing.T) {
	c, recorder := responsesPrecommitContext(t, true)
	body := `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}

`
	usage, newAPIError := OaiResponsesStreamHandler(c, responsesPrecommitInfo(), responsesHTTPResponse(body))

	require.Nil(t, newAPIError)
	require.Equal(t, &dto.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}, usage)
	require.Contains(t, recorder.Body.String(), `"type":"response.created"`)
	require.Contains(t, recorder.Body.String(), `"type":"response.completed"`)
}

func TestOrdinaryResponsesFailureKeepsExistingPassThroughBehavior(t *testing.T) {
	c, recorder := responsesPrecommitContext(t, false)
	_, newAPIError := OaiResponsesStreamHandler(c, responsesPrecommitInfo(), responsesHTTPResponse(
		`event: response.failed
data: {"type":"response.failed","response":{"status":"failed"}}

`,
	))

	require.Nil(t, newAPIError)
	require.True(t, c.Writer.Written())
	require.Contains(t, recorder.Body.String(), `"type":"response.failed"`)
}
