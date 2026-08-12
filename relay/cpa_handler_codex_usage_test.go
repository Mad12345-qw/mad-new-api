package relay

import (
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

func TestApplyCPAUsageTrailerPreservesExactUpstreamSplit(t *testing.T) {
	raw := `{"input_tokens":11,"output_tokens":7,"reasoning_tokens":2,"cached_tokens":3,"total_tokens":18}`
	response := &http.Response{Trailer: make(http.Header)}
	response.Trailer.Set(cpaUsageTrailer, hex.EncodeToString([]byte(raw)))
	usage := &dto.Usage{TotalTokens: 18}

	found, err := applyCPAUsageTrailer(response, usage)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 11, usage.PromptTokens)
	require.Equal(t, 7, usage.CompletionTokens)
	require.Equal(t, 18, usage.TotalTokens)
	require.Equal(t, 3, usage.PromptTokensDetails.CachedTokens)
	require.Equal(t, 2, usage.CompletionTokenDetails.ReasoningTokens)
	require.Equal(t, "cpa-official-upstream", usage.UsageSource)
}

func TestNormalizeCPAHandlerUsagePrefersValidTopLevelOverInvalidNestedUsage(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18,
		BillingUsage: &dto.BillingUsage{
			Source: dto.BillingUsageSourceOAIResponses, Semantic: dto.BillingUsageSemanticOpenAI,
			OpenAIUsage: &dto.Usage{},
		},
	}

	require.Error(t, normalizeCPAHandlerUsage(usage))
	require.False(t, service.ValidEffectiveUsage(usage))
}

func TestNormalizeCPAHandlerUsageRejectsMissingExactUsage(t *testing.T) {
	require.Error(t, normalizeCPAHandlerUsage(&dto.Usage{TotalTokens: 18}))
}
