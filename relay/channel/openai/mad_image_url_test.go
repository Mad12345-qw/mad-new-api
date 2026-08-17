package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestShouldNormalizeMadImageURLIsStrictlyGPTImageOnly(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		format string
		stream bool
		want   bool
	}{
		{name: "gpt image url", model: "gpt-image-2", format: "url", want: true},
		{name: "gpt image 4k url", model: "gpt-image-2-4k", format: "url", want: true},
		{name: "gpt image base64", model: "gpt-image-2", format: "b64_json", want: false},
		{name: "gpt image stream", model: "gpt-image-2", format: "url", stream: true, want: false},
		{name: "gemini remains untouched", model: "gemini-3.1-flash-image-preview", format: "url", want: false},
		{name: "grok remains untouched", model: "grok-imagine-image", format: "url", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				OriginModelName: test.model,
				IsStream:        test.stream,
				Request: &dto.ImageRequest{
					Model:          test.model,
					ResponseFormat: test.format,
				},
			}
			require.Equal(t, test.want, shouldNormalizeMadImageURL(info))
		})
	}
}

func TestNormalizeMadImageURLResponseCachesBase64AndReturnsOnlyMadURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cacheDir := t.TempDir()
	t.Setenv("MADAPI_IMAGE_CACHE_DIR", cacheDir)
	t.Setenv("MADAPI_PUBLIC_BASE_URL", "https://mad.myddns.me")
	madImageCleanupMu.Lock()
	madImageLastCleanup = madImageLastCleanup.Add(-madImageCleanupPeriod)
	madImageCleanupMu.Unlock()

	rawImage := append([]byte("\x89PNG\r\n\x1a\n"), []byte("madapi-test-image")...)
	encoded := base64.StdEncoding.EncodeToString(rawImage)
	body := []byte(`{"created":1786937240,"data":[{"b64_json":"` + encoded + `","revised_prompt":"kept"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "https://mad.myddns.me/v1/images/generations", nil)
	result, err := normalizeMadImageURLResponse(context, body)
	require.NoError(t, err)
	require.NotContains(t, string(result), "b64_json")
	require.NotContains(t, string(result), encoded)

	var decoded struct {
		Created int64 `json:"created"`
		Data    []struct {
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
		Usage map[string]int `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(result, &decoded))
	require.Equal(t, int64(1786937240), decoded.Created)
	require.Len(t, decoded.Data, 1)
	require.Equal(t, "kept", decoded.Data[0].RevisedPrompt)
	require.True(t, strings.HasPrefix(decoded.Data[0].URL, "https://mad.myddns.me/mad-media/images/"))
	require.Equal(t, 3, decoded.Usage["total_tokens"])

	filename := strings.TrimPrefix(decoded.Data[0].URL, "https://mad.myddns.me/mad-media/images/")
	cached, err := os.ReadFile(filepath.Join(cacheDir, filename))
	require.NoError(t, err)
	require.Equal(t, rawImage, cached)
}

type madImageRoundTripper func(*http.Request) (*http.Response, error)

func (fn madImageRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestNormalizeMadImageURLResponseHidesAndCachesUpstreamURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cacheDir := t.TempDir()
	t.Setenv("MADAPI_IMAGE_CACHE_DIR", cacheDir)
	t.Setenv("MADAPI_PUBLIC_BASE_URL", "https://mad.myddns.me")
	originalLookup := madImageLookupIP
	originalClient := madImageHTTPClient
	t.Cleanup(func() {
		madImageLookupIP = originalLookup
		madImageHTTPClient = originalClient
	})
	madImageLookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	rawImage := append([]byte("\x89PNG\r\n\x1a\n"), []byte("madapi-url-image")...)
	madImageHTTPClient = &http.Client{Transport: madImageRoundTripper(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, "https://upstream.example/private.png?signature=secret", request.URL.String())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(rawImage)),
			Request:    request,
		}, nil
	})}

	body := []byte(`{"created":1786937240,"data":[{"url":"https://upstream.example/private.png?signature=secret"}]}`)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "https://mad.myddns.me/v1/images/generations", nil)
	result, err := normalizeMadImageURLResponse(context, body)
	require.NoError(t, err)
	require.NotContains(t, string(result), "upstream.example")
	require.NotContains(t, string(result), "signature=secret")
	require.Contains(t, string(result), "https://mad.myddns.me/mad-media/images/")
}
