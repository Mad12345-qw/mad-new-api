package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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

func TestNormalizeCodexResponsesLiteDowngradesInvalidContract(t *testing.T) {
	t.Setenv("MADAPI_CODEX_LITE_COMPAT_ENABLED", "true")
	body := `{"model":"gpt-5.6-sol","reasoning":{"context":"current_turn"},"client_metadata":{"keep":"yes","ws_request_header_x_openai_internal_codex_responses_lite":"true"},"input":"hello"}`
	header, got := runCodexLiteMiddleware(t, body)
	require.Empty(t, header)
	require.False(t, gjson.GetBytes(got, codexResponsesLiteMetadata).Exists())
	require.Equal(t, "current_turn", gjson.GetBytes(got, "reasoning.context").String())
	require.Equal(t, "yes", gjson.GetBytes(got, "client_metadata.keep").String())
	require.Equal(t, "hello", gjson.GetBytes(got, "input").String())
}

func TestNormalizeCodexResponsesLitePreservesValidContract(t *testing.T) {
	t.Setenv("MADAPI_CODEX_LITE_COMPAT_ENABLED", "true")
	body := `{"reasoning":{"context":"all_turns"},"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`
	header, got := runCodexLiteMiddleware(t, body)
	require.Equal(t, "true", header)
	require.Equal(t, body, string(got))
}

func TestNormalizeCodexResponsesLiteCanBeDisabledForCPAUpgrade(t *testing.T) {
	t.Setenv("MADAPI_CODEX_LITE_COMPAT_ENABLED", "false")
	body := `{"reasoning":{"context":"current_turn"},"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`
	header, got := runCodexLiteMiddleware(t, body)
	require.Equal(t, "true", header)
	require.Equal(t, body, string(got))
}

func TestNormalizeCodexResponsesLiteDoesNotTouchStandardV1(t *testing.T) {
	t.Setenv("MADAPI_CODEX_LITE_COMPAT_ENABLED", "true")
	body := `{"reasoning":{"context":"current_turn"},"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`
	header, got := runCodexLiteMiddlewareAtPath(t, "/v1/responses", body)
	require.Equal(t, "true", header)
	require.Equal(t, body, string(got))
}

func runCodexLiteMiddleware(t *testing.T, body string) (string, []byte) {
	return runCodexLiteMiddlewareAtPath(t, "/codex/v1/responses", body)
}

func runCodexLiteMiddlewareAtPath(t *testing.T, path, body string) (string, []byte) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var header string
	var got []byte
	engine := gin.New()
	engine.Use(BodyStorageCleanup())
	engine.Use(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/codex/") {
			MarkCodexRoute()(c)
			return
		}
		c.Next()
	})
	engine.Use(NormalizeCodexResponsesLite())
	handler := func(c *gin.Context) {
		header = c.GetHeader(codexResponsesLiteHeader)
		storage, err := common.GetBodyStorage(c)
		require.NoError(t, err)
		got, err = storage.Bytes()
		require.NoError(t, err)
		c.Status(http.StatusNoContent)
	}
	engine.POST("/codex/v1/responses", handler)
	engine.POST("/v1/responses", handler)
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set(codexResponsesLiteHeader, "true")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusNoContent, recorder.Code)
	return header, got
}
