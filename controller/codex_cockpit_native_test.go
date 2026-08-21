package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRewriteCodexCockpitRequestBodyChangesOnlyModelShell(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	raw := []byte(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"keep this exact input"}]}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/codex/cockpit/v1/responses", bytes.NewReader(raw))

	rewritten, err := rewriteCodexCockpitRequestBody(c, raw)

	require.NoError(t, err)
	require.Equal(t, "claude-fable-5", gjson.GetBytes(rewritten, "model").String())
	require.Equal(t, gjson.GetBytes(raw, "input").Raw, gjson.GetBytes(rewritten, "input").Raw)
	require.Equal(t, gjson.GetBytes(raw, "stream").Bool(), gjson.GetBytes(rewritten, "stream").Bool())
}
