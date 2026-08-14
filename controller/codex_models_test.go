package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestCodexAPIShellOrderMatchesStableEightModelContract(t *testing.T) {
	order := codexAPIShellOrder()
	require.Len(t, order, 8)
	require.Equal(t, []string{
		"gpt-5.5", "gpt-5.4", "gpt-5.6-sol", "gpt-5.6-terra",
		"gpt-5.6-luna", "gpt-5.4-mini", "gpt-5.3-codex", "gpt-5.2",
	}, order)
	for _, shell := range order {
		require.NotEmpty(t, constant.CodexAPIModelSlots[shell])
	}
}

func TestCodexConversationCatalogFiltersMediaModels(t *testing.T) {
	for _, id := range []string{
		"gpt-image-2", "gemini-3-pro-image-preview", "seedance-2.0-fast",
		"grok-imagine-video", "sora-2", "veo-3", "kling-video", "hailuo-video",
	} {
		require.False(t, isCodexConversationModelID(id), id)
	}
	for _, id := range []string{
		"gpt-5.6-sol", "claude-opus-5", "gemini-3.6-flash", "grok-4.6",
	} {
		require.True(t, isCodexConversationModelID(id), id)
	}
}

func TestBuildNativeCodexModelKeepsClientCapabilityFields(t *testing.T) {
	entry := buildNativeCodexModel("gpt-5.5", "claude-fable-5", 1)
	require.Equal(t, "gpt-5.5", entry["slug"])
	require.Equal(t, "claude-fable-5", entry["display_name"])
	require.Equal(t, []string{"text", "image"}, entry["input_modalities"])
	require.Equal(t, true, entry["supports_parallel_tool_calls"])
	require.Equal(t, true, entry["supports_search_tool"])
	require.Equal(t, false, entry["prefer_websockets"])
}
