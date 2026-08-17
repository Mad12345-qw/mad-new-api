package gemini

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

type geminiResponsesBufferedTool struct {
	choiceIndex int
	toolIndex   int
	id          string
	name        string
	arguments   string
}

type geminiResponsesToolBuffer struct {
	tools   map[string]*geminiResponsesBufferedTool
	flushed bool
}

func newGeminiResponsesToolBuffer() *geminiResponsesToolBuffer {
	return &geminiResponsesToolBuffer{tools: make(map[string]*geminiResponsesBufferedTool)}
}

func (b *geminiResponsesToolBuffer) absorb(response *dto.ChatCompletionsStreamResponse) bool {
	if b == nil || response == nil || b.flushed {
		return false
	}
	found := false
	for choiceIndex := range response.Choices {
		choice := &response.Choices[choiceIndex]
		for toolPosition := range choice.Delta.ToolCalls {
			call := choice.Delta.ToolCalls[toolPosition]
			key := fmt.Sprintf("%d:%d", choice.Index, toolPosition)
			buffered := b.tools[key]
			if buffered == nil {
				buffered = &geminiResponsesBufferedTool{
					choiceIndex: choice.Index,
					toolIndex:   toolPosition,
					id:          call.ID,
				}
				b.tools[key] = buffered
			}
			if call.ID != "" && buffered.id == "" {
				buffered.id = call.ID
			}
			if call.Function.Name != "" {
				buffered.name = call.Function.Name
			}
			if call.Function.Arguments != "" {
				// Gemini streams complete functionCall snapshots. The newest snapshot is
				// authoritative; appending it would produce invalid JSON such as
				// {}{"value":"..."}.
				buffered.arguments = call.Function.Arguments
			}
			found = true
		}
		choice.Delta.ToolCalls = nil
	}
	return found
}

func (b *geminiResponsesToolBuffer) pending() bool {
	return b != nil && !b.flushed && len(b.tools) > 0
}

func (b *geminiResponsesToolBuffer) flush(responseID string, created int64, model string) *dto.ChatCompletionsStreamResponse {
	if !b.pending() {
		return nil
	}
	b.flushed = true
	tools := make([]*geminiResponsesBufferedTool, 0, len(b.tools))
	for _, tool := range b.tools {
		tools = append(tools, tool)
	}
	sort.SliceStable(tools, func(i, j int) bool {
		if tools[i].choiceIndex != tools[j].choiceIndex {
			return tools[i].choiceIndex < tools[j].choiceIndex
		}
		return tools[i].toolIndex < tools[j].toolIndex
	})

	toolCalls := make([]dto.ToolCallResponse, 0, len(tools))
	for index, tool := range tools {
		arguments := tool.arguments
		if arguments == "" {
			arguments = "{}"
		}
		call := dto.ToolCallResponse{
			ID:   tool.id,
			Type: "function",
			Function: dto.FunctionResponse{
				Name:      tool.name,
				Arguments: arguments,
			},
		}
		call.SetIndex(index)
		toolCalls = append(toolCalls, call)
	}
	finishReason := constant.FinishReasonToolCalls
	return &dto.ChatCompletionsStreamResponse{
		Id:      responseID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index: 0,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					ToolCalls: toolCalls,
				},
				FinishReason: &finishReason,
			},
		},
	}
}

func geminiResponsesStreamTerminal(response *dto.GeminiChatResponse) bool {
	if response == nil {
		return false
	}
	for _, candidate := range response.Candidates {
		if candidate.FinishReason != nil && *candidate.FinishReason != "" {
			return true
		}
	}
	return false
}

func clearGeminiResponsesToolFinish(response *dto.ChatCompletionsStreamResponse) {
	if response == nil {
		return
	}
	for index := range response.Choices {
		if response.Choices[index].FinishReason != nil && *response.Choices[index].FinishReason == constant.FinishReasonToolCalls {
			response.Choices[index].FinishReason = nil
		}
	}
}

func GeminiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	logger.LogDebug(c, "Gemini responses response body: %s", responseBody)

	var geminiResponse dto.GeminiChatResponse
	if err := common.Unmarshal(responseBody, &geminiResponse); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if len(geminiResponse.Candidates) == 0 {
		usage := buildUsageFromGeminiResponse(c, info, &geminiResponse)
		if geminiResponse.PromptFeedback != nil && geminiResponse.PromptFeedback.BlockReason != nil {
			common.SetContextKey(c, constant.ContextKeyAdminRejectReason, fmt.Sprintf("gemini_block_reason=%s", *geminiResponse.PromptFeedback.BlockReason))
			return &usage, types.NewOpenAIError(
				errors.New("request blocked by Gemini API: "+*geminiResponse.PromptFeedback.BlockReason),
				types.ErrorCodePromptBlocked,
				http.StatusBadRequest,
			)
		}
		common.SetContextKey(c, constant.ContextKeyAdminRejectReason, "gemini_empty_candidates")
		return &usage, types.NewOpenAIError(
			errors.New("empty response from Gemini API"),
			types.ErrorCodeEmptyResponse,
			http.StatusInternalServerError,
		)
	}

	chatResp := responseGeminiChat2OpenAI(c, &geminiResponse)
	chatResp.Model = info.UpstreamModelName
	if responseID := helper.GetResponseID(c); responseID != "" {
		chatResp.Id = responseID
	}
	usage := buildUsageFromGeminiResponse(c, info, &geminiResponse)
	chatResp.Usage = usage

	convertResult, err := relayconvert.ConvertResponse(c, info, types.RelayFormatOpenAIResponses, chatResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	responsesResp, ok := convertResult.Value.(*dto.OpenAIResponsesResponse)
	if !ok {
		return nil, types.NewOpenAIError(fmt.Errorf("expected OpenAI responses response, got %T", convertResult.Value), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	responsesUsage := convertResult.Usage
	if responsesUsage == nil || responsesUsage.TotalTokens == 0 {
		responsesResp.Usage = relayconvert.UsageFromChatUsage(&usage)
	}

	responseBody, err = common.Marshal(responsesResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	service.IOCopyBytesGracefully(c, resp, responseBody)
	return &usage, nil
}

func GeminiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	responseID := helper.GetResponseID(c)
	created := common.GetTimestamp()
	state, err := relayconvert.NewResponseStreamState(types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, relayconvert.ResponseStreamOptions{
		ID:      responseID,
		Model:   info.UpstreamModelName,
		Created: created,
	})
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	finishReason := constant.FinishReasonStop
	toolBuffer := newGeminiResponsesToolBuffer()
	var streamErr *types.NewAPIError

	sendEvent := func(event relayconvert.ChatToResponsesStreamEvent) bool {
		data, err := common.Marshal(event.Payload)
		if err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
			return false
		}
		helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: event.Type}, string(data))
		return true
	}
	sendChunk := func(chunk *dto.ChatCompletionsStreamResponse) bool {
		results, err := relayconvert.ConvertStreamResponseChunk(c, info, state, chunk)
		if err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
			return false
		}
		for _, result := range results {
			event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
			if !ok {
				streamErr = types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
				return false
			}
			if !sendEvent(event) {
				return false
			}
		}
		return true
	}

	usage, streamAPIError := geminiStreamHandler(c, info, resp, func(data string, geminiResponse *dto.GeminiChatResponse) bool {
		terminal := geminiResponsesStreamTerminal(geminiResponse)
		response, isStop := streamResponseGeminiChat2OpenAI(geminiResponse)
		response.Id = responseID
		response.Created = created
		response.Model = info.UpstreamModelName

		hasTools := toolBuffer.absorb(response)
		if hasTools || toolBuffer.pending() {
			finishReason = constant.FinishReasonToolCalls
		}
		if toolBuffer.pending() {
			clearGeminiResponsesToolFinish(response)
		}

		if !sendChunk(response) {
			return false
		}
		if terminal && toolBuffer.pending() {
			if !sendChunk(toolBuffer.flush(responseID, created, info.UpstreamModelName)) {
				return false
			}
		}
		if isStop {
			return sendChunk(helper.GenerateStopResponse(responseID, created, info.UpstreamModelName, finishReason))
		}
		return true
	})
	if streamAPIError != nil {
		return usage, streamAPIError
	}
	if streamErr != nil {
		return nil, streamErr
	}
	if toolBuffer.pending() {
		if !sendChunk(toolBuffer.flush(responseID, created, info.UpstreamModelName)) {
			return nil, streamErr
		}
	}

	if usage != nil {
		state.SetUsage(usage)
	}
	finalResults, err := relayconvert.FinalizeStreamResponse(c, info, state)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	for _, result := range finalResults {
		event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
		if !ok {
			return nil, types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
		}
		if !sendEvent(event) {
			return nil, streamErr
		}
	}
	return usage, nil
}
