package relay

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestDispatchToCPASDKUsesBinaryFrameWithoutEncodingPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload := []byte{0, 1, 2, 3, 254, 255}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "secret", r.Header.Get(appconstant.MadAPICPASDKDispatchHeader))
		var prefix [4]byte
		_, err := io.ReadFull(r.Body, prefix[:])
		require.NoError(t, err)
		metadata := make([]byte, binary.BigEndian.Uint32(prefix[:]))
		_, err = io.ReadFull(r.Body, metadata)
		require.NoError(t, err)
		var meta cpaSDKDispatchMeta
		require.NoError(t, json.Unmarshal(metadata, &meta))
		require.Equal(t, 66, meta.ChannelID)
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.True(t, bytes.Equal(payload, raw))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	t.Setenv(appconstant.MadAPICPASDKDispatchURLEnv, server.URL)
	t.Setenv(appconstant.MadAPICPASDKDispatchTokenEnv, "secret")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	response, err := dispatchToCPASDK(ctx, cpaSDKDispatchMeta{ChannelID: 66}, payload)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
}

func TestCPASDKSupportsImagesOnlyForOfficialImageExecutors(t *testing.T) {
	require.True(t, CPASDKSupportsImages(appconstant.ChannelTypeOpenAI))
	require.True(t, CPASDKSupportsImages(appconstant.ChannelTypeXai))
	require.True(t, CPASDKSupportsImages(appconstant.ChannelTypeCodex))
	require.True(t, CPASDKSupportsImages(appconstant.ChannelTypeNewAPI))

	require.False(t, CPASDKSupportsImages(appconstant.ChannelTypeGemini))
	require.False(t, CPASDKSupportsImages(appconstant.ChannelTypeVertexAi))
	require.False(t, CPASDKSupportsImages(appconstant.ChannelTypeAnthropic))
}

func TestStripCPASDKHopByHopHeaders(t *testing.T) {
	headers := http.Header{
		"Connection":        {"keep-alive, X-Remove-Me"},
		"Upgrade":           {"websocket"},
		"Keep-Alive":        {"timeout=5"},
		"Transfer-Encoding": {"chunked"},
		"Te":                {"trailers"},
		"X-Remove-Me":       {"connection-scoped"},
		"X-Keep-Me":         {"end-to-end"},
	}

	stripCPASDKHopByHopHeaders(headers)

	require.Equal(t, "end-to-end", headers.Get("X-Keep-Me"))
	for _, name := range []string{"Connection", "Upgrade", "Keep-Alive", "Transfer-Encoding", "Te", "X-Remove-Me"} {
		require.Empty(t, headers.Values(name), name)
	}
}
