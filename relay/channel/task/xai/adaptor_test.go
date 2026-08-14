package xai

import (
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestNormalizeOfficialXAIRequest(t *testing.T) {
	req, err := normalizeRequest(map[string]any{
		"model":        "grok-imagine-video-1.5",
		"prompt":       "A gold line moves across a black background",
		"duration":     float64(15),
		"aspect_ratio": "16:9",
		"resolution":   "1080p",
		"image": map[string]any{
			"url": "https://example.com/input.png",
		},
	}, nil, "")

	require.NoError(t, err)
	require.Equal(t, 15, req.Duration)
	require.Equal(t, "16:9", req.AspectRatio)
	require.Equal(t, "1080p", req.Resolution)
	require.NotNil(t, req.Image)
	require.Equal(t, "https://example.com/input.png", req.Image.URL)
}

func TestNormalizeOpenAIContentArrayRequest(t *testing.T) {
	req, err := normalizeRequest(map[string]any{
		"model":   "grok-imagine-video-1.5-preview",
		"seconds": "4",
		"size":    "1280x720",
		"content": []any{
			map[string]any{"type": "text", "text": "Animate the product photo"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
		},
	}, nil, "")

	require.NoError(t, err)
	require.Equal(t, "Animate the product photo", req.Prompt)
	require.Equal(t, 4, req.Duration)
	require.Equal(t, "720p", req.Resolution)
	require.Equal(t, "16:9", req.AspectRatio)
	require.NotNil(t, req.Image)
}

func TestNormalizeOfficialReferenceImagesRequest(t *testing.T) {
	req, err := normalizeRequest(map[string]any{
		"model":        "grok-imagine-video",
		"prompt":       "Use the people and clothing from the references",
		"duration":     float64(10),
		"resolution":   "720p",
		"aspect_ratio": "16:9",
		"reference_images": []any{
			map[string]any{"url": "https://example.com/person.png"},
			map[string]any{"url": "https://example.com/shirt.png"},
			map[string]any{"url": "https://example.com/location.png"},
		},
	}, nil, "")

	require.NoError(t, err)
	require.Nil(t, req.Image)
	require.Len(t, req.ReferenceImages, 3)
	require.Equal(t, "https://example.com/person.png", req.ReferenceImages[0].URL)
	require.Equal(t, "https://example.com/shirt.png", req.ReferenceImages[1].URL)
	require.Equal(t, "https://example.com/location.png", req.ReferenceImages[2].URL)
}

func TestNormalizeOfficialReferenceImageObjectArrayRequest(t *testing.T) {
	req, err := normalizeRequest(map[string]any{
		"model":      "grok-imagine-video",
		"prompt":     "Use the reference images",
		"duration":   float64(2),
		"resolution": "720p",
		"reference_images": []map[string]any{
			{"url": "https://example.com/person.png"},
			{"url": "https://example.com/product.png"},
		},
	}, nil, "")

	require.NoError(t, err)
	require.Nil(t, req.Image)
	require.Len(t, req.ReferenceImages, 2)
	require.Equal(t, "https://example.com/person.png", req.ReferenceImages[0].URL)
	require.Equal(t, "https://example.com/product.png", req.ReferenceImages[1].URL)
}

func TestNormalizeOfficialReferenceImageStringMapArrayRequest(t *testing.T) {
	req, err := normalizeRequest(map[string]any{
		"model":      "grok-imagine-video",
		"prompt":     "Use the reference images",
		"duration":   float64(2),
		"resolution": "720p",
		"reference_images": []map[string]string{
			{"url": "https://example.com/person.png"},
			{"url": "https://example.com/product.png"},
		},
	}, nil, "")

	require.NoError(t, err)
	require.Nil(t, req.Image)
	require.Len(t, req.ReferenceImages, 2)
	require.Equal(t, "https://example.com/person.png", req.ReferenceImages[0].URL)
	require.Equal(t, "https://example.com/product.png", req.ReferenceImages[1].URL)
}

func TestNormalizeOfficialReferenceImageStructArrayRequest(t *testing.T) {
	req, err := normalizeRequest(map[string]any{
		"model":      "grok-imagine-video",
		"prompt":     "Use the reference images",
		"duration":   float64(2),
		"resolution": "720p",
		"reference_images": []imageInput{
			{URL: "https://example.com/person.png"},
			{URL: "https://example.com/product.png"},
		},
	}, nil, "")

	require.NoError(t, err)
	require.Nil(t, req.Image)
	require.Len(t, req.ReferenceImages, 2)
	require.Equal(t, "https://example.com/person.png", req.ReferenceImages[0].URL)
	require.Equal(t, "https://example.com/product.png", req.ReferenceImages[1].URL)
}

func TestNormalizeMainstreamMultiImageContentAsReferences(t *testing.T) {
	req, err := normalizeRequest(map[string]any{
		"model":    "grok-imagine-video",
		"prompt":   "Combine the references",
		"duration": 2,
		"content": []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/one.png"}},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/two.png"}},
		},
	}, nil, "")

	require.NoError(t, err)
	require.Nil(t, req.Image)
	require.Len(t, req.ReferenceImages, 2)
}

func TestNormalizeRejectsInvalidReferenceImageModes(t *testing.T) {
	eightImages := make([]any, 8)
	for i := range eightImages {
		eightImages[i] = map[string]any{"url": "https://example.com/reference-" + string(rune('a'+i)) + ".png"}
	}
	tests := []map[string]any{
		{
			"model": "grok-imagine-video-1.5", "prompt": "unsupported model", "duration": 2,
			"reference_images": []any{"https://example.com/one.png"},
		},
		{
			"model": "grok-imagine-video", "prompt": "too many", "duration": 2,
			"reference_images": eightImages,
		},
		{
			"model": "grok-imagine-video", "prompt": "too long", "duration": 11,
			"reference_images": []any{"https://example.com/one.png"},
		},
		{
			"model": "grok-imagine-video", "prompt": "mixed modes", "duration": 2,
			"image": "https://example.com/start.png", "reference_images": []any{"https://example.com/reference.png"},
		},
	}
	for _, input := range tests {
		_, err := normalizeRequest(input, nil, "")
		require.Error(t, err)
	}
}

func TestNormalizeUsesFiveSecondAnd720pDefaults(t *testing.T) {
	req, err := normalizeRequest(map[string]any{
		"model":  "grok-imagine-video",
		"prompt": "Default request",
	}, nil, "")

	require.NoError(t, err)
	require.Equal(t, 5, req.Duration)
	require.Equal(t, "720p", req.Resolution)
}

func TestNormalizeRejectsUnsupportedDurationAndResolution(t *testing.T) {
	for _, input := range []map[string]any{
		{"model": "grok-imagine-video-1.5", "prompt": "zero duration", "duration": 0},
		{"model": "grok-imagine-video-1.5", "prompt": "too short", "duration": -1},
		{"model": "grok-imagine-video-1.5", "prompt": "too long", "duration": 16},
		{"model": "grok-imagine-video-1.5", "prompt": "fractional duration", "duration": 4.5},
		{"model": "grok-imagine-video-1.5", "prompt": "removed resolution", "resolution": "480p"},
		{"model": "grok-imagine-video-1.5", "prompt": "1080p text only", "duration": 2, "resolution": "1080p"},
		{"model": "grok-imagine-video", "prompt": "unsupported resolution", "resolution": "1080p"},
	} {
		_, err := normalizeRequest(input, nil, "")
		require.Error(t, err)
	}
}

func TestEstimateBillingCountsEveryReferenceImage(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(normalizedRequestKey, generationRequest{
		Model:      "grok-imagine-video",
		Duration:   2,
		Resolution: "720p",
		ReferenceImages: []imageInput{
			{URL: "https://example.com/one.png"},
			{URL: "https://example.com/two.png"},
			{URL: "https://example.com/three.png"},
		},
	})

	ratios := (&TaskAdaptor{}).EstimateBilling(c, &relaycommon.RelayInfo{})

	require.Equal(t, 2.0, ratios["seconds"])
	require.Equal(t, 1.0, ratios["resolution"])
	require.Equal(t, 3.0, ratios["images"])
}

func TestEstimateBillingUsesExactGrokRatios(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(normalizedRequestKey, generationRequest{
		Model:      "grok-imagine-video-1.5",
		Duration:   15,
		Resolution: "1080p",
		Image:      &imageInput{URL: "https://example.com/input.png"},
	})

	ratios := (&TaskAdaptor{}).EstimateBilling(c, &relaycommon.RelayInfo{})

	require.Equal(t, 15.0, ratios["seconds"])
	require.InDelta(t, 25.0/14.0, ratios["resolution"], 0.0000001)
	require.Equal(t, 1.0, ratios["images"])
}

func TestParseTaskResultReadsNestedVideoURL(t *testing.T) {
	result, err := (&TaskAdaptor{}).ParseTaskResult([]byte(`{
		"request_id":"req_123",
		"status":"done",
		"video":{"url":"https://cdn.example.com/result.mp4","duration":5}
	}`))

	require.NoError(t, err)
	require.Equal(t, "SUCCESS", string(result.Status))
	require.Equal(t, "https://cdn.example.com/result.mp4", result.Url)
}

func TestParseTaskResultHandlesOfficialTerminalErrors(t *testing.T) {
	for _, status := range []string{"failed", "expired"} {
		result, err := (&TaskAdaptor{}).ParseTaskResult([]byte(`{
			"request_id":"req_123",
			"status":"` + status + `",
			"error":{"message":"upstream rejected the request"}
		}`))

		require.NoError(t, err)
		require.Equal(t, "FAILURE", string(result.Status))
		require.Equal(t, "upstream rejected the request", result.Reason)
	}
}
