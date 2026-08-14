package relay

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/task/sora"
	taskxai "github.com/QuantumNous/new-api/relay/channel/task/xai"
	"github.com/stretchr/testify/require"
)

func TestGetTaskPlatformForApiOkSeedanceModels(t *testing.T) {
	models := []string{
		"seedance-2.0",
		"seedance-2.0-fast",
		"doubao-seedance-2.0-720p",
		"doubao-seedance-2.0-cf-1080p",
	}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			platform := GetTaskPlatformForModel(
				constant.TaskPlatform("54"),
				model,
			)
			require.Equal(t, constant.TaskPlatformApiOkSeedance, platform)
		})
	}
}

func TestGetApiOkSeedanceTaskAdaptor(t *testing.T) {
	adaptor := GetTaskAdaptor(constant.TaskPlatformApiOkSeedance)
	require.NotNil(t, adaptor)

	soraAdaptor, ok := adaptor.(*sora.TaskAdaptor)
	require.True(t, ok)
	require.True(t, soraAdaptor.FixedPrice)
}

func TestGetTaskPlatformForOtherModelKeepsChannelPlatform(t *testing.T) {
	platform := constant.TaskPlatform("54")
	require.Equal(
		t,
		platform,
		GetTaskPlatformForModel(platform, "doubao-seedance-2-0-260128"),
	)
}

func TestGetTaskPlatformForGrokVideoModels(t *testing.T) {
	for _, modelName := range []string{
		"grok-imagine-video",
		"grok-imagine-video-1.5",
		"grok-imagine-video-1.5-preview",
	} {
		require.Equal(t, constant.TaskPlatformXAI, GetTaskPlatformForModel(constant.TaskPlatform("48"), modelName))
	}
}

func TestGetXAITaskAdaptor(t *testing.T) {
	adaptor := GetTaskAdaptor(constant.TaskPlatformXAI)
	require.NotNil(t, adaptor)
	_, ok := adaptor.(*taskxai.TaskAdaptor)
	require.True(t, ok)
}
