package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	appconstant "github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestShouldUseCodexOpenAIResponsesFastPathIsStrictlyScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newContext := func(remoteAddr, marker string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Request.RemoteAddr = remoteAddr
		c.Request.Header.Set("X-MadAPI-Codex-Compat", marker)
		return c
	}
	newInfo := func(apiType, relayMode int) *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{
			RelayMode: relayMode,
			ChannelMeta: &relaycommon.ChannelMeta{
				ApiType: apiType,
			},
		}
	}

	t.Setenv("MADAPI_CODEX_OPENAI_RESPONSES_FAST_PATH", "true")
	internal := newContext("127.0.0.1:12345", relayconvert.CodexResponsesInternalMarker())
	require.True(t, shouldUseCodexOpenAIResponsesFastPath(internal, newInfo(appconstant.APITypeOpenAI, relayconstant.RelayModeResponses)))
	require.False(t, shouldUseCodexOpenAIResponsesFastPath(internal, newInfo(appconstant.APITypeXai, relayconstant.RelayModeResponses)))
	require.False(t, shouldUseCodexOpenAIResponsesFastPath(internal, newInfo(appconstant.APITypeOpenAI, relayconstant.RelayModeResponsesCompact)))

	external := newContext("203.0.113.10:12345", relayconvert.CodexResponsesInternalMarker())
	require.False(t, shouldUseCodexOpenAIResponsesFastPath(external, newInfo(appconstant.APITypeOpenAI, relayconstant.RelayModeResponses)))

	forged := newContext("127.0.0.1:12345", "forged-marker")
	require.False(t, shouldUseCodexOpenAIResponsesFastPath(forged, newInfo(appconstant.APITypeOpenAI, relayconstant.RelayModeResponses)))

	t.Setenv("MADAPI_CODEX_OPENAI_RESPONSES_FAST_PATH", "false")
	require.False(t, shouldUseCodexOpenAIResponsesFastPath(internal, newInfo(appconstant.APITypeOpenAI, relayconstant.RelayModeResponses)))
}
