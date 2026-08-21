package gemini

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func geminiImageResponseContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "https://madclaude.cloud/v1/images/generations", nil)
	context.Request.Host = "madclaude.cloud"
	context.Request.Header.Set("X-Forwarded-Proto", "https")
	return context, recorder
}

func geminiImageHTTPResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func TestGeminiImagePreviewResponseUsesMadURLsWithoutBase64(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cacheDir := t.TempDir()
	t.Setenv("MADAPI_IMAGE_CACHE_DIR", cacheDir)
	context, recorder := geminiImageResponseContext(t)

	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x11}, 32)...)
	jpeg := append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{0x22}, 32)...)
	body := fmt.Sprintf(`{
  "candidates":[{"content":{"parts":[
    {"inlineData":{"mimeType":"image/png","data":"%s"}},
    {"inline_data":{"mime_type":"image/jpeg","data":"%s"}}
  ]}}],
  "usageMetadata":{"promptTokenCount":604,"candidatesTokenCount":1120,"totalTokenCount":1724}
}`,
		base64.StdEncoding.EncodeToString(png),
		base64.StdEncoding.EncodeToString(jpeg),
	)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAIImage,
		RelayMode:   relayconstant.RelayModeImagesGenerations,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.1-flash-image-preview"},
	}

	usage, apiError := GeminiImagePreviewHandler(context, info, geminiImageHTTPResponse(body))
	require.Nil(t, apiError)
	require.Equal(t, 604, usage.PromptTokens)
	require.Equal(t, 1120, usage.CompletionTokens)
	require.Equal(t, 1724, usage.TotalTokens)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "candidates")
	require.NotContains(t, recorder.Body.String(), "inlineData")
	require.NotContains(t, recorder.Body.String(), "inline_data")
	require.NotContains(t, recorder.Body.String(), "b64_json")
	require.NotContains(t, recorder.Body.String(), base64.StdEncoding.EncodeToString(png))

	var output struct {
		Created int64 `json:"created"`
		Data    []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &output))
	require.Positive(t, output.Created)
	require.Len(t, output.Data, 2)

	for index, expected := range [][]byte{png, jpeg} {
		parsed, err := url.Parse(output.Data[index].URL)
		require.NoError(t, err)
		require.Equal(t, "https", parsed.Scheme)
		require.Equal(t, "madclaude.cloud", parsed.Host)
		require.Contains(t, parsed.Path, "/mad-media/images/")
		cached, err := os.ReadFile(filepath.Join(cacheDir, filepath.Base(parsed.Path)))
		require.NoError(t, err)
		require.Equal(t, expected, cached)
	}
}

func TestGeminiImagePreviewResponseRemovesCachedFilesWhenJSONFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cacheDir := t.TempDir()
	t.Setenv("MADAPI_IMAGE_CACHE_DIR", cacheDir)
	context, _ := geminiImageResponseContext(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x33}, 32)...)
	body := fmt.Sprintf(
		`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"%s"}}]}}],`,
		base64.StdEncoding.EncodeToString(png),
	)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAIImage,
		RelayMode:   relayconstant.RelayModeImagesGenerations,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3-pro-image-preview"},
	}

	_, apiError := GeminiImagePreviewHandler(context, info, geminiImageHTTPResponse(body))
	require.NotNil(t, apiError)
	entries, err := os.ReadDir(cacheDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestGeminiImagePreviewResponseDispatchIsStrict(t *testing.T) {
	tests := []struct {
		name string
		info *relaycommon.RelayInfo
		want bool
	}{
		{
			name: "image generation",
			info: &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIImage, RelayMode: relayconstant.RelayModeImagesGenerations, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.1-flash-image-preview"}},
			want: true,
		},
		{
			name: "image edit",
			info: &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIImage, RelayMode: relayconstant.RelayModeImagesEdits, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3-pro-image-preview"}},
			want: true,
		},
		{
			name: "native Gemini image returns URL envelope",
			info: &relaycommon.RelayInfo{RelayFormat: types.RelayFormatGemini, RelayMode: relayconstant.RelayModeGemini, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.1-flash-image-preview"}},
			want: true,
		},
		{
			name: "ordinary chat stays chat",
			info: &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.1-flash-image-preview"}},
		},
		{
			name: "Imagen keeps existing handler",
			info: &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIImage, RelayMode: relayconstant.RelayModeImagesGenerations, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "imagen-4"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, shouldHandleGeminiImagePreviewResponse(test.info))
		})
	}
}

func TestGeminiImagePreviewResponseNeverReturnsExplicitBase64(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cacheDir := t.TempDir()
	t.Setenv("MADAPI_IMAGE_CACHE_DIR", cacheDir)
	context, recorder := geminiImageResponseContext(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x44}, 32)...)
	body := fmt.Sprintf(
		`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"%s"}}]}}],"usageMetadata":{"totalTokenCount":1}}`,
		base64.StdEncoding.EncodeToString(png),
	)
	request := &dto.ImageRequest{Model: "gemini-3-pro-image-preview", ResponseFormat: "b64_json"}
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAIImage,
		RelayMode:   relayconstant.RelayModeImagesGenerations,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3-pro-image-preview"},
		Request:     request,
	}

	_, apiError := GeminiImagePreviewHandler(context, info, geminiImageHTTPResponse(body))
	require.Nil(t, apiError)
	require.Contains(t, recorder.Body.String(), `"url":"https://madclaude.cloud/mad-media/images/`)
	require.NotContains(t, recorder.Body.String(), "b64_json")
}
