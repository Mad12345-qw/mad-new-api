package service

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
)

func TestValidEffectiveUsageRejectsZeroNestedBillingUsage(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     11,
		CompletionTokens: 7,
		TotalTokens:      18,
		BillingUsage: &dto.BillingUsage{
			Source:      dto.BillingUsageSourceOAIResponses,
			Semantic:    dto.BillingUsageSemanticOpenAI,
			OpenAIUsage: &dto.Usage{},
		},
	}

	require.True(t, ValidUsage(usage))
	require.False(t, ValidEffectiveUsage(usage))

	usage.BillingUsage = nil
	require.True(t, ValidEffectiveUsage(usage))
}
