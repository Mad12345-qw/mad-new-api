package relay

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestCloneOpenAIResponsesRequestMatchesDeepCopyAndIsolatesMutableFields(t *testing.T) {
	stream := true
	temperature := 0.4
	maxOutputTokens := uint(4096)
	largeInput := json.RawMessage(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]`)
	source := &dto.OpenAIResponsesRequest{
		Model:            "gpt-5.6-luna-high",
		Input:            largeInput,
		Instructions:     json.RawMessage(`"answer in Chinese"`),
		MaxOutputTokens:  &maxOutputTokens,
		Reasoning:        &dto.Reasoning{Effort: "high", Summary: "auto"},
		Stream:           &stream,
		StreamOptions:    &dto.StreamOptions{IncludeUsage: true, IncludeObfuscation: true},
		Temperature:      &temperature,
		Tools:            json.RawMessage(`[{"type":"web_search"}]`),
		SafetyIdentifier: json.RawMessage(`"client-1"`),
		ServiceTier:      "priority",
	}

	expected, err := common.DeepCopy(source)
	require.NoError(t, err)
	actual := cloneOpenAIResponsesRequest(source)
	require.Equal(t, expected, actual)

	actual.Model = "mapped-model"
	actual.Reasoning.Effort = "low"
	actual.StreamOptions.IncludeUsage = false
	*actual.MaxOutputTokens = 1
	*actual.Stream = false
	*actual.Temperature = 1

	require.Equal(t, "gpt-5.6-luna-high", source.Model)
	require.Equal(t, "high", source.Reasoning.Effort)
	require.True(t, source.StreamOptions.IncludeUsage)
	require.Equal(t, uint(4096), *source.MaxOutputTokens)
	require.True(t, *source.Stream)
	require.Equal(t, 0.4, *source.Temperature)
	require.True(t, bytes.Equal(source.Input, actual.Input))
	require.Equal(t, &source.Input[0], &actual.Input[0], "large raw payload must be reused without copying")
}

func TestFilterOpenAIResponsesRequestFieldsMatchesGenericFilter(t *testing.T) {
	tests := []struct {
		name     string
		settings dto.ChannelOtherSettings
	}{
		{name: "defaults"},
		{name: "allow all", settings: dto.ChannelOtherSettings{AllowServiceTier: true, AllowSafetyIdentifier: true, AllowIncludeObfuscation: true}},
		{name: "disable store", settings: dto.ChannelOtherSettings{DisableStore: true}},
		{name: "keep usage only", settings: dto.ChannelOtherSettings{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := dto.OpenAIResponsesRequest{
				Model:            "gpt-5.6-luna",
				Input:            json.RawMessage(`[{"role":"user","content":"hello"}]`),
				ServiceTier:      "priority",
				Store:            json.RawMessage(`false`),
				SafetyIdentifier: json.RawMessage(`"client-1"`),
				StreamOptions:    &dto.StreamOptions{IncludeUsage: true, IncludeObfuscation: true},
			}

			originalJSON, err := common.Marshal(request)
			require.NoError(t, err)
			expectedJSON, err := relaycommon.RemoveDisabledFields(originalJSON, test.settings, false)
			require.NoError(t, err)

			filtered, complete := filterOpenAIResponsesRequestFields(request, test.settings)
			require.True(t, complete)
			actualJSON, err := common.Marshal(filtered)
			require.NoError(t, err)

			require.JSONEq(t, string(expectedJSON), string(actualJSON))
		})
	}
}

func BenchmarkCloneOpenAIResponsesRequest(b *testing.B) {
	input := json.RawMessage(bytes.Repeat([]byte(`{"type":"input_text","text":"0123456789abcdef"},`), 200000))
	request := &dto.OpenAIResponsesRequest{
		Model:         "gpt-5.6-luna",
		Input:         input,
		Reasoning:     &dto.Reasoning{Effort: "high", Summary: "auto"},
		StreamOptions: &dto.StreamOptions{IncludeUsage: true},
	}

	b.Run("generic-deep-copy", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			cloned, err := common.DeepCopy(request)
			if err != nil || cloned == nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("responses-light-clone", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if cloneOpenAIResponsesRequest(request) == nil {
				b.Fatal("nil clone")
			}
		}
	})
}
