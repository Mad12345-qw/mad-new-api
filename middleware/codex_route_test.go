package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMarkCodexRouteRestoresStandardV1Path(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses/compact", nil)

	MarkCodexRoute()(ctx)

	require.True(t, IsCodexRoute(ctx))
	require.Equal(t, "/v1/responses/compact", ctx.Request.URL.Path)
}

func TestMarkCodexRouteDoesNotRewriteOtherPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	MarkCodexRoute()(ctx)

	require.Equal(t, "/v1/responses", ctx.Request.URL.Path)
}

func TestCodexCompactDoesNotUseSyntheticChannelModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/codex/v1/responses/compact", bytes.NewBufferString(`{"model":"gpt-5.6-terra","input":"hello"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	MarkCodexRoute()(ctx)
	request, _, err := getModelRequest(ctx)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-terra", request.Model)
}
