package gemini

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
)

func TestConvertGeminiImagePreviewRequest(t *testing.T) {
	request, err := convertGeminiImagePreviewRequest(dto.ImageRequest{
		Prompt:  "draw a test image",
		Size:    "16:9",
		Quality: "2K",
	})
	require.NoError(t, err)
	require.Len(t, request.Contents, 1)
	require.Equal(t, "user", request.Contents[0].Role)
	require.Equal(t, "draw a test image", request.Contents[0].Parts[0].Text)
	require.Equal(t, []string{"TEXT", "IMAGE"}, request.GenerationConfig.ResponseModalities)
	require.JSONEq(t, `{"aspectRatio":"16:9","imageSize":"2K"}`, string(request.GenerationConfig.ImageConfig))
}

func TestGeminiImagePreviewModelMatrix(t *testing.T) {
	require.True(t, isGeminiImagePreviewModel("gemini-3.1-flash-image-preview"))
	require.True(t, isGeminiImagePreviewModel("gemini-3-pro-image-preview"))
	require.False(t, isGeminiImagePreviewModel("gemini-3.1-flash-image-preview-4K"))
	require.False(t, isGeminiImagePreviewModel("gemini-3-pro-image-preview-4K"))
	require.False(t, isGeminiImagePreviewModel("imagen-4"))
}

func TestGeminiImagePreviewResolution(t *testing.T) {
	require.Equal(t, "4K", geminiImageSize(dto.ImageRequest{Quality: "4K"}))
	require.Equal(t, "2K", geminiImageSize(dto.ImageRequest{Quality: "high"}))
	require.Equal(t, "1K", geminiImageSize(dto.ImageRequest{}))
}
