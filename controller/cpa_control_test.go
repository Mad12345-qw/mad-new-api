package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestCPAProviderMappingUsesUpstreamProtocolNotChannelBrand(t *testing.T) {
	tests := []struct {
		apiType int
		provider string
	}{
		{constant.APITypeCodex, "codex"},
		{constant.APITypeOpenAI, "openai-compatibility"},
		{constant.APITypeNewAPI, "openai-compatibility"},
		{constant.APITypeSub2API, "openai-compatibility"},
		{constant.APITypeAnthropic, "claude"},
		{constant.APITypeGemini, "gemini"},
		{constant.APITypeXai, "xai"},
	}
	for _, test := range tests {
		provider, err := cpaProviderForAPIType(test.apiType)
		require.NoError(t, err)
		require.Equal(t, test.provider, provider)
	}
	_, err := cpaProviderForAPIType(constant.APITypeAdvancedCustom)
	require.Error(t, err, "unverified protocol mappings must fail closed")
}

func TestCPAOpenAICompatibilityBaseURLHasV1Boundary(t *testing.T) {
	require.Equal(t, "https://example.com/v1", cpaNormalizedBaseURL("https://example.com", "openai-compatibility"))
	require.Equal(t, "https://example.com/v1", cpaNormalizedBaseURL("https://example.com/v1/", "openai-compatibility"))
	require.Equal(t, "https://chatgpt.com/backend-api/codex", cpaNormalizedBaseURL("https://chatgpt.com/backend-api/codex", "codex"))
}
