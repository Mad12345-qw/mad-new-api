package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestPrepareResponsesCompactFallbackPreservesExistingInstructions(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{Instructions: []byte(`"keep project constraints"`)}
	prepareResponsesCompactFallback(req)
	var instructions string
	require.NoError(t, json.Unmarshal(req.Instructions, &instructions))
	assert.Contains(t, instructions, nativeCompactInstruction)
	assert.Contains(t, instructions, "keep project constraints")
}

func TestResponsesCompactFallbackHandlerNormalizesXAIResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	upstream := `{"id":"resp_123","object":"response","created_at":123,"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"remember ORCHID"}]}],"usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}}`
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(upstream)), Header: make(http.Header)}
	usage, apiErr := responsesCompactFallbackHandler(c, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ApiType: constant.APITypeXai}}, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 16, usage.TotalTokens)
	assert.Equal(t, "cmp_123", gjson.GetBytes(recorder.Body.Bytes(), "id").String())
	assert.Equal(t, "response.compaction", gjson.GetBytes(recorder.Body.Bytes(), "object").String())
	assert.Equal(t, "compaction_summary", gjson.GetBytes(recorder.Body.Bytes(), "output.0.type").String())
	assert.Equal(t, "remember ORCHID", gjson.GetBytes(recorder.Body.Bytes(), "output.0.summary").String())
	assert.Equal(t, int64(16), gjson.GetBytes(recorder.Body.Bytes(), "usage.total_tokens").Int())
}

func TestResponsesCompactFallbackHandlerRejectsMissingUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	upstream := `{"id":"resp_123","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"summary"}]}]}`
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(upstream)), Header: make(http.Header)}
	usage, apiErr := responsesCompactFallbackHandler(c, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ApiType: constant.APITypeXai}}, resp)
	assert.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Contains(t, apiErr.Error(), "omitted exact usage")
}
