package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func CodexChatToResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	var chatResponse dto.OpenAITextResponse
	if err := common.Unmarshal(body, &chatResponse); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if openAIError := chatResponse.GetOpenAIError(); openAIError != nil && openAIError.Type != "" {
		return nil, types.WithOpenAIError(*openAIError, resp.StatusCode)
	}
	usage := &chatResponse.Usage
	if !service.ValidUsage(usage) {
		var responseText strings.Builder
		for _, choice := range chatResponse.Choices {
			responseText.WriteString(choice.Message.StringContent())
			responseText.WriteString(choice.Message.GetReasoningContent())
		}
		usage = service.ResponseText2Usage(c, responseText.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
	}
	applyUsagePostProcessing(info, usage, body)

	converted := relayconvert.ConvertCodexChatResponseToResponsesNonStream(
		c.Request.Context(), info.UpstreamModelName, nil, nil, body,
	)
	if !json.Valid(converted) {
		return nil, types.NewOpenAIError(fmt.Errorf("Codex compatibility conversion returned invalid non-stream Responses JSON"), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	service.IOCopyBytesGracefully(c, resp, converted)
	return usage, nil
}

func CodexChatToResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	var conversionState any
	var usage *dto.Usage
	var responseText strings.Builder
	var streamErr *types.NewAPIError

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	writeChunks := func(chunks [][]byte) bool {
		for _, chunk := range chunks {
			if len(chunk) == 0 {
				continue
			}
			newline := bytes.IndexByte(chunk, '\n')
			if newline <= len("event: ") || !bytes.HasPrefix(chunk, []byte("event: ")) || !bytes.HasPrefix(chunk[newline+1:], []byte("data: ")) {
				streamErr = types.NewOpenAIError(fmt.Errorf("Codex compatibility conversion returned an invalid Responses SSE frame"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
				return false
			}
			eventType := string(bytes.TrimSpace(chunk[len("event: "):newline]))
			payload := bytes.TrimSpace(chunk[newline+1+len("data: "):])
			if err := helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: eventType}, string(payload)); err != nil {
				streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
				return false
			}
		}
		return true
	}

	helper.StreamScannerHandler(c, resp, info, func(data string, result *helper.StreamResult) {
		var chunk dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
			logger.LogError(c, "failed to unmarshal Codex chat stream response: "+err.Error())
			result.Error(err)
			return
		}
		if chunk.Usage != nil && service.ValidUsage(chunk.Usage) {
			usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			responseText.WriteString(choice.Delta.GetContentString())
			responseText.WriteString(choice.Delta.GetReasoningContent())
		}
		rawLine := append([]byte("data: "), []byte(data)...)
		chunks := relayconvert.ConvertCodexChatResponseToResponsesStream(
			c.Request.Context(), info.UpstreamModelName, nil, nil, rawLine, &conversionState,
		)
		if !writeChunks(chunks) {
			result.Stop(streamErr)
		}
	})
	if streamErr != nil {
		return nil, streamErr
	}

	if usage == nil || !service.ValidUsage(usage) {
		usage = service.ResponseText2Usage(c, responseText.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
	}
	applyUsagePostProcessing(info, usage, nil)
	finalChunks := relayconvert.ConvertCodexChatResponseToResponsesStream(
		c.Request.Context(), info.UpstreamModelName, nil, nil, []byte("data: [DONE]"), &conversionState,
	)
	if !writeChunks(finalChunks) {
		return nil, streamErr
	}
	return usage, nil
}
