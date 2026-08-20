package relay

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func TestCloneResponsesRequestForRelayKeepsRetrySourcePristine(t *testing.T) {
	original := &dto.OpenAIResponsesRequest{
		Model:     "client-model",
		Input:     json.RawMessage(`[{"type":"message","role":"user","content":"hello"}]`),
		Tools:     json.RawMessage(`[{"type":"web_search"}]`),
		Reasoning: &dto.Reasoning{Effort: "high"},
	}

	firstAttempt := cloneResponsesRequestForRelay(original)
	firstAttempt.Model = "first-upstream-model"
	firstAttempt.Reasoning.Effort = "low"
	secondAttempt := cloneResponsesRequestForRelay(original)

	require.Equal(t, "client-model", original.Model)
	require.Equal(t, "high", original.Reasoning.Effort)
	require.Equal(t, "client-model", secondAttempt.Model)
	require.Equal(t, "high", secondAttempt.Reasoning.Effort)
	require.Equal(t, string(original.Input), string(secondAttempt.Input))
	require.Equal(t, string(original.Tools), string(secondAttempt.Tools))
}
