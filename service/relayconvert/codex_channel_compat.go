package relayconvert

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	codexresponses "github.com/QuantumNous/new-api/service/relayconvert/internal/codex_responses"
)

func ConvertCodexResponsesRequestToChatRequest(request dto.OpenAIResponsesRequest) (*dto.GeneralOpenAIRequest, error) {
	raw, err := common.Marshal(&request)
	if err != nil {
		return nil, err
	}
	stream := request.Stream != nil && *request.Stream
	converted := codexresponses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(request.Model, raw, stream)
	var chatRequest dto.GeneralOpenAIRequest
	if err := common.Unmarshal(converted, &chatRequest); err != nil {
		return nil, fmt.Errorf("Codex Responses request conversion failed: %w", err)
	}
	if stream {
		chatRequest.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
	}
	return &chatRequest, nil
}

func ConvertCodexChatResponseToResponsesNonStream(ctx context.Context, modelName string, originalRequest, convertedRequest, response []byte) []byte {
	return codexresponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(
		ctx, modelName, originalRequest, convertedRequest, response, nil,
	)
}

func ConvertCodexChatResponseToResponsesStream(ctx context.Context, modelName string, originalRequest, convertedRequest, response []byte, state *any) [][]byte {
	return codexresponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(
		ctx, modelName, originalRequest, convertedRequest, response, state,
	)
}
