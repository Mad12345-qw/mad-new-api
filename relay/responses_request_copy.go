package relay

import (
	"github.com/QuantumNous/new-api/dto"
)

// cloneOpenAIResponsesRequest creates an attempt-local request without walking
// the large, read-only JSON payloads. Relays only replace RawMessage fields;
// they never modify their backing bytes in place. The mutable pointer fields
// are copied so model mapping and provider normalization cannot change the
// original request that a later channel retry starts from.
func cloneOpenAIResponsesRequest(source *dto.OpenAIResponsesRequest) *dto.OpenAIResponsesRequest {
	if source == nil {
		return nil
	}

	cloned := *source
	cloned.MaxOutputTokens = clonePointer(source.MaxOutputTokens)
	cloned.TopLogProbs = clonePointer(source.TopLogProbs)
	cloned.Stream = clonePointer(source.Stream)
	cloned.Temperature = clonePointer(source.Temperature)
	cloned.TopP = clonePointer(source.TopP)
	cloned.MaxToolCalls = clonePointer(source.MaxToolCalls)

	if source.Reasoning != nil {
		reasoning := *source.Reasoning
		cloned.Reasoning = &reasoning
	}
	if source.StreamOptions != nil {
		streamOptions := *source.StreamOptions
		cloned.StreamOptions = &streamOptions
	}

	return &cloned
}

func clonePointer[T any](source *T) *T {
	if source == nil {
		return nil
	}
	cloned := *source
	return &cloned
}

// filterOpenAIResponsesRequestFields applies the same top-level filtering as
// RemoveDisabledFields before marshaling. This avoids parsing and rebuilding a
// very large Responses body merely to remove a handful of known fields.
//
// The boolean result is true only when the converted value is a Responses DTO,
// whose complete set of removable fields is represented below. Other provider
// request types continue through the existing generic filter unchanged.
func filterOpenAIResponsesRequestFields(converted any, settings dto.ChannelOtherSettings) (any, bool) {
	filter := func(request dto.OpenAIResponsesRequest) dto.OpenAIResponsesRequest {
		if !settings.AllowServiceTier {
			request.ServiceTier = ""
		}
		if settings.DisableStore {
			request.Store = nil
		}
		if !settings.AllowSafetyIdentifier {
			request.SafetyIdentifier = nil
		}
		if !settings.AllowIncludeObfuscation && request.StreamOptions != nil && request.StreamOptions.IncludeObfuscation {
			streamOptions := *request.StreamOptions
			streamOptions.IncludeObfuscation = false
			if !streamOptions.IncludeUsage {
				request.StreamOptions = nil
			} else {
				request.StreamOptions = &streamOptions
			}
		}
		return request
	}

	switch request := converted.(type) {
	case dto.OpenAIResponsesRequest:
		return filter(request), true
	case *dto.OpenAIResponsesRequest:
		if request == nil {
			return request, true
		}
		filtered := filter(*request)
		return &filtered, true
	default:
		return converted, false
	}
}
