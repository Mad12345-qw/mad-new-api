package sora

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestParseTaskResultTreatsRequestIDAsQueued(t *testing.T) {
	adaptor := &TaskAdaptor{}

	result, err := adaptor.ParseTaskResult([]byte(`{
		"request_id":"e6d4926a-c606-9315-8679-36faf6b52a29",
		"id":"task_PjJ0t1qaaehsEWWXQb05Fr2m984p9WH8"
	}`))

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, model.TaskStatusQueued, result.Status)
}

func TestParseTaskResultKeepsCompletedVideoURL(t *testing.T) {
	adaptor := &TaskAdaptor{}

	result, err := adaptor.ParseTaskResult([]byte(`{
		"id":"video_42",
		"status":"completed",
		"video_url":"https://cdn.example.com/output.mp4"
	}`))

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, model.TaskStatusSuccess, result.Status)
	require.Equal(t, "https://cdn.example.com/output.mp4", result.Url)
}

func TestParseTaskResultKeepsApiOkUnknownTaskQueued(t *testing.T) {
	adaptor := &TaskAdaptor{}

	result, err := adaptor.ParseTaskResult([]byte(`{
		"id":"task_uNh4k93xSiVAGEtAspAPFEzdj8ZjS6jZ",
		"model":"doubao-seedance-2.0-cf-1080p",
		"status":"unknown",
		"progress":0
	}`))

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, model.TaskStatusQueued, result.Status)
}

func TestIsApiOkSeedanceModel(t *testing.T) {
	require.True(t, IsApiOkSeedanceModel("seedance-2.0"))
	require.True(t, IsApiOkSeedanceModel("DOUBAO-SEEDANCE-2.0-CF-1080P"))
	require.False(t, IsApiOkSeedanceModel("doubao-seedance-2-0-260128"))
}

func TestMikotoSeedanceModelsAreIsolatedFromLegacyPricingModels(t *testing.T) {
	require.True(t, IsMikotoSeedanceModel("seedance-2.0-1080p"))
	require.True(t, IsMikotoSeedanceModel("seedance-2.0-720p"))
	require.True(t, IsMikotoSeedanceModel("seedance-fast-720p"))

	require.False(t, IsMikotoSeedanceModel("seedance-2.0"))
	require.False(t, IsMikotoSeedanceModel("seedance-2.0-fast"))
	require.False(t, IsMikotoSeedanceModel("doubao-seedance-2.0-720p"))
	require.False(t, IsMikotoSeedanceModel("doubao-seedance-2.0-cf-1080p"))
	require.False(t, IsApiOkSeedanceModel("seedance-2.0-1080p"))
}

func TestNormalizeMikotoSeedanceOpenAIRequest(t *testing.T) {
	input := map[string]interface{}{
		"model":           "seedance-2.0-720p",
		"prompt":          "Keep the exact prompt",
		"seconds":         "6",
		"size":            "720x1280",
		"input_reference": "data:image/png;base64,AAAA",
	}

	request, err := normalizeMikotoSeedanceMap(input, "seedance-2.0-720p")
	require.NoError(t, err)
	require.Equal(t, "Keep the exact prompt", request.Prompt)
	require.Equal(t, 6, request.Duration)
	require.Equal(t, "9:16", request.AspectRatio)
	require.Equal(t, []string{"data:image/png;base64,AAAA"}, request.Images)
}

func TestNormalizeMikotoSeedanceDoubaoContent(t *testing.T) {
	inputJSON := `{
		"model":"seedance-2.0-1080p",
		"duration":5,
		"ratio":"16:9",
		"content":[
			{"type":"text","text":"Preserve this text"},
			{"type":"image_url","image_url":{"url":"https://example.com/a.png"}},
			{"type":"video_url","video_url":{"url":"https://example.com/a.mp4"}},
			{"type":"audio_url","audio_url":{"url":"https://example.com/a.mp3"}}
		],
		"generate_audio":true
	}`
	var input map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(inputJSON), &input))

	request, err := normalizeMikotoSeedanceMap(input, "seedance-2.0-1080p")
	require.NoError(t, err)
	require.Equal(t, "Preserve this text", request.Prompt)
	require.Equal(t, 5, request.Duration)
	require.Equal(t, "16:9", request.AspectRatio)
	require.Equal(t, []string{"https://example.com/a.png"}, request.Images)
	require.Equal(t, []string{"https://example.com/a.mp4"}, request.ReferenceVideos)
	require.Equal(t, []string{"https://example.com/a.mp3"}, request.ReferenceAudios)
	require.NotNil(t, request.GenerateAudio)
	require.True(t, *request.GenerateAudio)
}

func TestNormalizeMikotoSeedanceNativeReferenceArrays(t *testing.T) {
	input := map[string]interface{}{
		"prompt":           "Native request",
		"duration":         float64(15),
		"aspect_ratio":     "3:4",
		"images":           []interface{}{"https://example.com/1.png", "https://example.com/2.png", "https://example.com/3.png"},
		"referenceVideos": []interface{}{"https://example.com/1.mp4"},
		"referenceAudios": []interface{}{"https://example.com/1.mp3"},
	}

	request, err := normalizeMikotoSeedanceMap(input, "seedance-fast-720p")
	require.NoError(t, err)
	require.Equal(t, 15, request.Duration)
	require.Equal(t, "media", request.ReferenceMode)
	require.Len(t, request.Images, 3)
	require.Len(t, request.ReferenceVideos, 1)
	require.Len(t, request.ReferenceAudios, 1)
}

func TestNormalizeMikotoSeedanceRejectsInvalidDuration(t *testing.T) {
	for _, duration := range []interface{}{nil, float64(3), float64(16), "4.5"} {
		input := map[string]interface{}{
			"prompt":       "duration validation",
			"aspect_ratio": "16:9",
		}
		if duration != nil {
			input["duration"] = duration
		}
		_, err := normalizeMikotoSeedanceMap(input, "seedance-fast-720p")
		require.Error(t, err)
	}
}

func TestNormalizeMikotoSeedanceRejectsUnsupportedRatio(t *testing.T) {
	_, err := normalizeMikotoSeedanceMap(map[string]interface{}{
		"prompt":       "unsupported ratio",
		"duration":     float64(4),
		"aspect_ratio": "21:9",
	}, "seedance-fast-720p")
	require.Error(t, err)
}

func TestApiOkSeedanceContent(t *testing.T) {
	content := apiOkSeedanceContent(
		"doubao-seedance-2.0-cf-1080p",
		"make a four-second live-action video",
		nil,
	)
	require.Equal(
		t,
		[]ContentItem{{Type: "text", Text: "make a four-second live-action video"}},
		content,
	)

	existing := []interface{}{map[string]interface{}{"type": "text", "text": "existing"}}
	require.Equal(t, existing, apiOkSeedanceContent("seedance-2.0", "prompt", existing))
	require.Nil(t, apiOkSeedanceContent("sora-2", "prompt", nil))
	require.JSONEq(
		t,
		`[{"type":"text","text":"prompt"}]`,
		apiOkSeedanceMultipartContent("seedance-2.0", "prompt", ""),
	)
}
