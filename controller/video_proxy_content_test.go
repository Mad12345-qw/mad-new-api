package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/require"
)

func TestCopyVideoRangeHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("Range", "bytes=100-200")
	src.Set("If-Range", "etag-value")
	dst := http.Header{}

	copyVideoRangeHeaders(dst, src)

	require.Equal(t, "bytes=100-200", dst.Get("Range"))
	require.Equal(t, "etag-value", dst.Get("If-Range"))
}

func TestCopyVideoResponseHeadersOnlyCopiesStreamingMetadata(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "video/mp4")
	src.Set("Content-Range", "bytes 100-200/1000")
	src.Set("Content-Length", "101")
	src.Set("Accept-Ranges", "bytes")
	src.Set("Connection", "keep-alive")
	dst := http.Header{}

	copyVideoResponseHeaders(dst, src)

	require.Equal(t, "video/mp4", dst.Get("Content-Type"))
	require.Equal(t, "bytes 100-200/1000", dst.Get("Content-Range"))
	require.Equal(t, "101", dst.Get("Content-Length"))
	require.Equal(t, "bytes", dst.Get("Accept-Ranges"))
	require.Empty(t, dst.Get("Connection"))
}

func TestParseSingleByteRange(t *testing.T) {
	start, end, ok := parseSingleByteRange("bytes=0-0", 1000)
	require.True(t, ok)
	require.Equal(t, int64(0), start)
	require.Equal(t, int64(0), end)

	start, end, ok = parseSingleByteRange("bytes=100-", 1000)
	require.True(t, ok)
	require.Equal(t, int64(100), start)
	require.Equal(t, int64(999), end)

	start, end, ok = parseSingleByteRange("bytes=-25", 1000)
	require.True(t, ok)
	require.Equal(t, int64(975), start)
	require.Equal(t, int64(999), end)

	_, _, ok = parseSingleByteRange("bytes=1000-1001", 1000)
	require.False(t, ok)
}

func TestValidateSignedVideoURL(t *testing.T) {
	now := int64(1_800_000_000)
	expires := "1800000600"
	tokens := []*model.Token{
		{Key: "activekey", Status: common.TokenStatusEnabled},
		{Key: "disabledkey", Status: common.TokenStatusDisabled},
	}

	signature := videoSignature("activekey", "task_public", expires)
	require.True(t, validateSignedVideoURL("task_public", expires, signature, tokens, now))
	require.False(t, validateSignedVideoURL("task_other", expires, signature, tokens, now))
	require.False(t, validateSignedVideoURL("task_public", "1799999999", signature, tokens, now))

	disabledSignature := videoSignature("disabledkey", "task_public", expires)
	require.False(t, validateSignedVideoURL("task_public", expires, disabledSignature, tokens, now))
}

func TestStoredVideoResultURLPrefersPrivateResult(t *testing.T) {
	task := &model.Task{
		PrivateData: model.TaskPrivateData{ResultURL: " https://cdn.example.com/current.mp4 "},
		FailReason:  "https://cdn.example.com/legacy.mp4",
	}

	require.Equal(t, "https://cdn.example.com/current.mp4", storedVideoResultURL(task))
}

func TestStoredVideoResultURLSupportsLegacySuccessURL(t *testing.T) {
	task := &model.Task{FailReason: "https://cdn.example.com/legacy.mp4"}

	require.Equal(t, "https://cdn.example.com/legacy.mp4", storedVideoResultURL(task))
	require.Empty(t, storedVideoResultURL(&model.Task{FailReason: "completed"}))
	require.Empty(t, storedVideoResultURL(nil))
}

func TestResolveXaiVideoResultURLUsesChannelOrigin(t *testing.T) {
	resolved, err := resolveXaiVideoResultURL(
		"https://api.example.com/provider/",
		"/v1/videos/task_upstream/content",
	)

	require.NoError(t, err)
	require.Equal(t, "https://api.example.com/v1/videos/task_upstream/content", resolved)
}

func TestResolveXaiVideoResultURLRejectsProtocolRelativeHost(t *testing.T) {
	_, err := resolveXaiVideoResultURL(
		"https://api.example.com",
		"//untrusted.example.com/video.mp4",
	)

	require.Error(t, err)
}

func TestExtractVideoURLFromContentResponse(t *testing.T) {
	url := extractVideoURLFromContentResponse([]byte(`{
		"status":"done",
		"video":{"url":"https://vidgen.x.ai/example.mp4","duration":5}
	}`))

	require.Equal(t, "https://vidgen.x.ai/example.mp4", url)
}

func TestExtractVideoURLFromContentResponseRejectsMissingURL(t *testing.T) {
	require.Empty(t, extractVideoURLFromContentResponse([]byte(`{"status":"pending"}`)))
}
