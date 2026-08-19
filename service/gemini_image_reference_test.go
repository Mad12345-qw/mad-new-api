package service

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func TestBuildGeminiImageReferencePartsPreservesOrder(t *testing.T) {
	media := []dto.GeminiPart{
		{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "first"}},
		{FileData: &dto.GeminiFileData{MimeType: "image/jpeg", FileUri: "https://example.test/second.jpg"}},
	}
	parts := BuildGeminiImageReferenceParts("以图1为人物，以图2为服装", media)

	require.Len(t, parts, 6)
	require.Contains(t, parts[0].Text, "不得根据图片内容重新排序")
	require.Contains(t, parts[1].Text, "图1")
	require.Equal(t, "first", parts[2].InlineData.Data)
	require.Contains(t, parts[3].Text, "图2")
	require.Equal(t, "https://example.test/second.jpg", parts[4].FileData.FileUri)
	require.Contains(t, parts[5].Text, "以图1为人物，以图2为服装")
}

func TestNormalizeNativeGeminiImageReferences(t *testing.T) {
	request := &dto.GeminiChatRequest{
		Contents: []dto.GeminiChatContent{{
			Role: "user",
			Parts: []dto.GeminiPart{
				{Text: "以图1为人物"},
				{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "first"}},
				{Text: "并使用图2的服装"},
				{InlineData: &dto.GeminiInlineData{MimeType: "image/jpeg", Data: "second"}},
			},
		}},
		GenerationConfig: dto.GeminiChatGenerationConfig{
			ImageConfig: []byte(`{"aspectRatio":"3:4","imageSize":"4K"}`),
		},
	}

	count, err := NormalizeNativeGeminiImageReferences(request)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.Len(t, request.Contents[0].Parts, 6)
	require.Equal(t, "first", request.Contents[0].Parts[2].InlineData.Data)
	require.Equal(t, "second", request.Contents[0].Parts[4].InlineData.Data)
	require.Contains(t, request.Contents[0].Parts[5].Text, "以图1为人物\n并使用图2的服装")
	require.JSONEq(t, `{"aspectRatio":"3:4","imageSize":"4K"}`, string(request.GenerationConfig.ImageConfig))
}

func TestNormalizeNativeGeminiImageReferencesRejectsMissingReference(t *testing.T) {
	request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{
		Role: "user",
		Parts: []dto.GeminiPart{
			{Text: "以图1为人物，并使用图4的服装"},
			{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "first"}},
		},
	}}}

	count, err := NormalizeNativeGeminiImageReferences(request)
	require.Zero(t, count)
	require.EqualError(t, err, "prompt references image 4 but request contains 1 reference image(s)")
}

func TestNormalizeNativeGeminiImageReferencesIsIdempotent(t *testing.T) {
	request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{
		Role: "user",
		Parts: []dto.GeminiPart{
			{Text: "use image 1"},
			{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "first"}},
		},
	}}}

	normalized, err := NormalizeNativeGeminiImageReferences(request)
	require.NoError(t, err)
	require.Equal(t, 1, normalized)
	firstLength := len(request.Contents[0].Parts)
	normalized, err = NormalizeNativeGeminiImageReferences(request)
	require.NoError(t, err)
	require.Zero(t, normalized)
	require.Len(t, request.Contents[0].Parts, firstLength)
}

func TestNormalizeNativeGeminiImageReferencesLeavesHistoryUntouched(t *testing.T) {
	request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{
		{
			Role: "user",
			Parts: []dto.GeminiPart{
				{Text: "historical image"},
				{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "history"}},
			},
		},
		{
			Role:  "model",
			Parts: []dto.GeminiPart{{Text: "previous answer"}},
		},
		{
			Role: "user",
			Parts: []dto.GeminiPart{
				{Text: "以图1为人物"},
				{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "current"}},
			},
		},
	}}

	normalized, err := NormalizeNativeGeminiImageReferences(request)
	require.NoError(t, err)
	require.Equal(t, 1, normalized)
	require.Len(t, request.Contents[0].Parts, 2)
	require.Equal(t, "history", request.Contents[0].Parts[1].InlineData.Data)
	require.Equal(t, "current", request.Contents[2].Parts[2].InlineData.Data)
}

func TestNormalizeNativeGeminiImageReferencesLeavesStructuredContentUntouched(t *testing.T) {
	request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{
		Role: "user",
		Parts: []dto.GeminiPart{
			{Text: "use image 1"},
			{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "first"}},
			{FunctionCall: &dto.FunctionCall{FunctionName: "lookup"}},
		},
	}}}

	normalized, err := NormalizeNativeGeminiImageReferences(request)
	require.NoError(t, err)
	require.Zero(t, normalized)
	require.Len(t, request.Contents[0].Parts, 3)
}

func TestIsGeminiImagePreviewModelMatchesFutureFamilyVersions(t *testing.T) {
	require.True(t, IsGeminiImagePreviewModel("gemini-3.1-flash-image-preview"))
	require.True(t, IsGeminiImagePreviewModel("gemini-3.2-pro-image-preview"))
	require.True(t, IsGeminiImagePreviewModel("gemini-3.2-pro-image-preview-4K"))
	require.False(t, IsGeminiImagePreviewModel("gemini-3.2-pro"))
	require.False(t, IsGeminiImagePreviewModel("imagen-4"))
}
