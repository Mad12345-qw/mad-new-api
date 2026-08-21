package relay

import (
	"encoding/json"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/stretchr/testify/require"
)

func TestShouldPassThroughNativeGeminiImageRequest(t *testing.T) {
	require.True(t, shouldPassThroughNativeGeminiImageRequest(&relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeGemini,
		OriginModelName: "gemini-3.1-flash-image-preview",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.1-flash-image-preview"},
	}))
	require.True(t, shouldPassThroughNativeGeminiImageRequest(&relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeGemini,
		OriginModelName: "gemini-3-pro-image-preview",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3-pro-image-preview"},
	}))
	require.False(t, shouldPassThroughNativeGeminiImageRequest(&relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeImagesEdits,
		OriginModelName: "gemini-3.1-flash-image-preview",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.1-flash-image-preview"},
	}))
	require.False(t, shouldPassThroughNativeGeminiImageRequest(&relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeGemini,
		OriginModelName: "gemini-2.5-pro",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-2.5-pro"},
	}))
}

func TestCanonicalizeNativeGeminiImageJSONPreservesOfficialBody(t *testing.T) {
	original := []byte(`{"contents":[{"role":"user","parts":[{"text":"keep prompt unchanged"},{"inlineData":{"mimeType":"image/png","data":"first"}}]}],"generationConfig":{"responseModalities":["TEXT","IMAGE"],"imageConfig":{"imageSize":"2K","aspectRatio":"3:4"}},"unknownTop":{"keep":true}}`)
	normalized, changed, err := canonicalizeNativeGeminiImageJSON(original)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, original, normalized)
}

func TestCanonicalizeNativeGeminiImageJSONNormalizesAliasesWithoutChangingSemantics(t *testing.T) {
	original := []byte(`{"contents":[{"role":"user","parts":[{"text":"图1人物，图2提花"},{"inline_data":{"mime_type":"image/png","data":"first"}},{"inline_data":{"mime_type":"image/jpeg","data":"second"}}]}],"generation_config":{"response_modalities":["TEXT","IMAGE"],"image_config":{"image_size":"2K","aspect_ratio":"3:4"}},"unknownTop":{"keep":true}}`)
	normalized, changed, err := canonicalizeNativeGeminiImageJSON(original)
	require.NoError(t, err)
	require.True(t, changed)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(normalized, &payload))
	contents := payload["contents"].([]any)
	parts := contents[0].(map[string]any)["parts"].([]any)
	require.Equal(t, "图1人物，图2提花", parts[0].(map[string]any)["text"])
	require.Equal(t, "first", parts[1].(map[string]any)["inlineData"].(map[string]any)["data"])
	require.Equal(t, "second", parts[2].(map[string]any)["inlineData"].(map[string]any)["data"])
	require.Equal(t, "image/png", parts[1].(map[string]any)["inlineData"].(map[string]any)["mimeType"])
	config := payload["generationConfig"].(map[string]any)
	imageConfig := config["imageConfig"].(map[string]any)
	require.Equal(t, "2K", imageConfig["imageSize"])
	require.Equal(t, "3:4", imageConfig["aspectRatio"])
	require.Equal(t, true, payload["unknownTop"].(map[string]any)["keep"])
	require.NotContains(t, string(normalized), "generation_config")
	require.NotContains(t, string(normalized), "inline_data")
}

func TestCanonicalizeNativeGeminiImageJSONRejectsConflictingAliases(t *testing.T) {
	_, _, err := canonicalizeNativeGeminiImageJSON([]byte(`{"generationConfig":{"imageConfig":{"imageSize":"1K"}},"generation_config":{"image_config":{"image_size":"2K"}}}`))
	require.ErrorContains(t, err, "conflicting Gemini fields")
}
