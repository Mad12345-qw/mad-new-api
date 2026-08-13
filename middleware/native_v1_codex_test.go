package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNativeCodexClientMapsAllCockpitShellModelsWithoutHeaders(t *testing.T) {
	for shell, target := range constant.CodexAPIModelSlots {
		t.Run(shell, func(t *testing.T) {
			marked, codexRoute, model := runNativeCodexMiddleware(t, "/codex/cockpit/v1/responses", "", shell)
			require.True(t, marked)
			require.True(t, codexRoute)
			require.Equal(t, target, model)
		})
	}
}

func TestNativeCodexClientDefaultsEstablishedRouteToOAuth(t *testing.T) {
	marked, codexRoute, model := runNativeCodexMiddleware(t, "/codex/v1/responses", "", "claude-sonnet-5")
	require.True(t, marked)
	require.True(t, codexRoute)
	require.Equal(t, "claude-sonnet-5", model)
}

func TestNativeCodexClientExplicitLoginModeOverridesPathDefault(t *testing.T) {
	marked, _, model := runNativeCodexMiddleware(t, "/codex/v1/responses", "apikey", "gpt-5.5")
	require.True(t, marked)
	require.Equal(t, "claude-fable-5", model)
}

func TestNativeCodexClientNeverMarksOrdinaryV1(t *testing.T) {
	marked, codexRoute, model := runNativeCodexMiddleware(t, "/v1/responses", "apikey", "gpt-5.5")
	require.False(t, marked)
	require.False(t, codexRoute)
	require.Equal(t, "gpt-5.5", model)
}

func runNativeCodexMiddleware(t *testing.T, path, loginMode, model string) (bool, bool, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var marked bool
	var codexRoute bool
	var got []byte
	engine := gin.New()
	engine.Use(BodyStorageCleanup())
	markCodex := func(c *gin.Context) {
		MarkCodexRoute()(c)
	}
	engine.Use(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/codex/") {
			markCodex(c)
			return
		}
		c.Next()
	})
	engine.Use(NativeV1CodexClient())
	handler := func(c *gin.Context) {
		marked = relaycommon.IsNativeV1CodexClient(c)
		codexRoute = IsCodexRoute(c)
		storage, err := common.GetBodyStorage(c)
		require.NoError(t, err)
		got, err = storage.Bytes()
		require.NoError(t, err)
		c.Status(http.StatusNoContent)
	}
	engine.POST("/codex/v1/responses", handler)
	engine.POST("/codex/cockpit/v1/responses", handler)
	engine.POST("/v1/responses", handler)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"`+model+`","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	if loginMode != "" {
		req.Header.Set(cpaLoginModeHeader, loginMode)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusNoContent, recorder.Code)
	return marked, codexRoute, gjson.GetBytes(got, "model").String()
}
