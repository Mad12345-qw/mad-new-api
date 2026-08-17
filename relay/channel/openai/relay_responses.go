package openai

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
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

const maxCodexResponsesPrecommitBytes = 32 << 20

type replayReadCloser struct {
	io.Reader
	io.Closer
}

func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	if responsesResponse.HasImageGenerationCall() {
		c.Set("image_generation_call", true)
		c.Set("image_generation_call_quality", responsesResponse.GetQuality())
		c.Set("image_generation_call_size", responsesResponse.GetSize())
	}

	// 写入新的 response body
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// compute usage
	usage := dto.Usage{}
	if responsesResponse.Usage != nil {
		usage.PromptTokens = responsesResponse.Usage.InputTokens
		usage.CompletionTokens = responsesResponse.Usage.OutputTokens
		usage.TotalTokens = responsesResponse.Usage.TotalTokens
		if responsesResponse.Usage.InputTokensDetails != nil {
			usage.PromptTokensDetails.CachedTokens = responsesResponse.Usage.InputTokensDetails.CachedTokens
			usage.PromptTokensDetails.CacheWriteTokens = responsesResponse.Usage.InputTokensDetails.CacheWriteTokens
		}
	}
	if info == nil || info.ResponsesUsageInfo == nil || info.ResponsesUsageInfo.BuiltInTools == nil {
		return &usage, nil
	}
	// 解析 Tools 用量
	for _, tool := range responsesResponse.Tools {
		buildToolinfo, ok := info.ResponsesUsageInfo.BuiltInTools[common.Interface2String(tool["type"])]
		if !ok || buildToolinfo == nil {
			logger.LogError(c, fmt.Sprintf("BuiltInTools not found for tool type: %v", tool["type"]))
			continue
		}
		buildToolinfo.CallCount++
	}
	return &usage, nil
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, "invalid response or response body")
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	if relayconvert.IsCodexResponsesInternalRequest(c) {
		if precommitErr := inspectCodexResponsesFirstEvent(resp); precommitErr != nil {
			return nil, precommitErr
		}
	}

	var usage = &dto.Usage{}
	var responseTextBuilder strings.Builder

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {

		// 检查当前数据是否包含 completed 状态和 usage 信息
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			logger.LogError(c, "failed to unmarshal stream response: "+err.Error())
			sr.Error(err)
			return
		}
		sendResponsesStreamData(c, streamResponse, data)
		switch streamResponse.Type {
		case "response.completed":
			if streamResponse.Response != nil {
				if streamResponse.Response.Usage != nil {
					if streamResponse.Response.Usage.InputTokens != 0 {
						usage.PromptTokens = streamResponse.Response.Usage.InputTokens
					}
					if streamResponse.Response.Usage.OutputTokens != 0 {
						usage.CompletionTokens = streamResponse.Response.Usage.OutputTokens
					}
					if streamResponse.Response.Usage.TotalTokens != 0 {
						usage.TotalTokens = streamResponse.Response.Usage.TotalTokens
					}
					if streamResponse.Response.Usage.InputTokensDetails != nil {
						usage.PromptTokensDetails.CachedTokens = streamResponse.Response.Usage.InputTokensDetails.CachedTokens
						usage.PromptTokensDetails.CacheWriteTokens = streamResponse.Response.Usage.InputTokensDetails.CacheWriteTokens
					}
				}
				if streamResponse.Response.HasImageGenerationCall() {
					c.Set("image_generation_call", true)
					c.Set("image_generation_call_quality", streamResponse.Response.GetQuality())
					c.Set("image_generation_call_size", streamResponse.Response.GetSize())
				}
			}
		case "response.output_text.delta":
			// 处理输出文本
			responseTextBuilder.WriteString(streamResponse.Delta)
		case dto.ResponsesOutputTypeItemDone:
			// 函数调用处理
			if streamResponse.Item != nil {
				switch streamResponse.Item.Type {
				case dto.BuildInCallWebSearchCall:
					if info != nil && info.ResponsesUsageInfo != nil && info.ResponsesUsageInfo.BuiltInTools != nil {
						if webSearchTool, exists := info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview]; exists && webSearchTool != nil {
							webSearchTool.CallCount++
						}
					}
				}
			}
		}
	})

	if usage.CompletionTokens == 0 {
		// 计算输出文本的 token 数量
		tempStr := responseTextBuilder.String()
		if len(tempStr) > 0 {
			// 非正常结束，使用输出文本的 token 数量
			completionTokens := service.CountTextToken(tempStr, info.UpstreamModelName)
			usage.CompletionTokens = completionTokens
		}
	}

	if usage.PromptTokens == 0 && usage.CompletionTokens != 0 {
		usage.PromptTokens = info.GetEstimatePromptTokens()
	}

	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

	return usage, nil
}

// inspectCodexResponsesFirstEvent implements the same first-chunk commit
// boundary used by CPA: a Codex-only Responses stream is not exposed to the
// downstream client until its first upstream data event is known to be usable.
// A failure event at that boundary is returned to NewAPI's existing retry loop;
// after any successful event is observed, the original byte stream is replayed
// unchanged and all later events keep their existing streaming behavior.
func inspectCodexResponsesFirstEvent(resp *http.Response) *types.NewAPIError {
	if resp == nil || resp.Body == nil {
		return types.NewErrorWithStatusCode(
			errors.New("upstream Responses stream omitted its body"),
			types.ErrorCodeBadResponseBody,
			http.StatusBadGateway,
		)
	}

	originalBody := resp.Body
	reader := bufio.NewReaderSize(originalBody, 64<<10)
	var prefix bytes.Buffer
	sseEventType := ""

	for {
		lineStart := prefix.Len()
		var readErr error
		for {
			fragment, err := reader.ReadSlice('\n')
			if prefix.Len()+len(fragment) > maxCodexResponsesPrecommitBytes {
				return types.NewErrorWithStatusCode(
					errors.New("upstream Responses first event exceeded the precommit limit"),
					types.ErrorCodeBadResponseBody,
					http.StatusBadGateway,
				)
			}
			_, _ = prefix.Write(fragment)
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			readErr = err
			break
		}

		line := bytes.TrimSpace(prefix.Bytes()[lineStart:])
		if bytes.HasPrefix(line, []byte("event:")) {
			sseEventType = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if len(data) > 0 {
				if bytes.Equal(data, []byte("[DONE]")) {
					return types.NewErrorWithStatusCode(
						errors.New("upstream Responses stream ended before its first event"),
						types.ErrorCodeBadResponseBody,
						http.StatusBadGateway,
					)
				}

				var event map[string]any
				if err := common.Unmarshal(data, &event); err != nil {
					return types.NewErrorWithStatusCode(
						fmt.Errorf("invalid upstream Responses first event: %w", err),
						types.ErrorCodeBadResponseBody,
						http.StatusBadGateway,
					)
				}
				eventType, _ := event["type"].(string)
				if strings.TrimSpace(eventType) == "" {
					eventType = sseEventType
				}
				switch strings.ToLower(strings.TrimSpace(eventType)) {
				case "error", "response.error", "response.failed":
					return codexResponsesFailureError(event)
				default:
					resp.Body = &replayReadCloser{
						Reader: io.MultiReader(bytes.NewReader(prefix.Bytes()), reader),
						Closer: originalBody,
					}
					return nil
				}
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return types.NewErrorWithStatusCode(
					errors.New("upstream Responses stream ended before its first event"),
					types.ErrorCodeBadResponseBody,
					http.StatusBadGateway,
				)
			}
			return types.NewErrorWithStatusCode(
				fmt.Errorf("failed to read upstream Responses first event: %w", readErr),
				types.ErrorCodeReadResponseBodyFailed,
				http.StatusBadGateway,
			)
		}
	}
}

func codexResponsesFailureError(event map[string]any) *types.NewAPIError {
	errorValue := event["error"]
	response, _ := event["response"].(map[string]any)
	if responseError, exists := response["error"]; exists {
		errorValue = responseError
	}
	openAIError := dto.GetOpenAIError(errorValue)
	if openAIError == nil {
		openAIError = &types.OpenAIError{
			Message: "upstream Responses stream failed before its first event",
			Type:    "upstream_error",
			Code:    types.ErrorCodeBadResponseBody,
		}
	}
	if strings.TrimSpace(openAIError.Message) == "" {
		openAIError.Message = "upstream Responses stream failed before its first event"
	}
	return types.WithOpenAIError(*openAIError, codexResponsesFailureStatus(event, response, openAIError))
}

func codexResponsesFailureStatus(event, response map[string]any, openAIError *types.OpenAIError) int {
	for _, source := range []map[string]any{event, response} {
		if source == nil {
			continue
		}
		for _, key := range []string{"status_code", "http_status", "status"} {
			if status := responsesHTTPStatus(source[key]); status != 0 {
				return status
			}
		}
		if errorObject, ok := source["error"].(map[string]any); ok {
			for _, key := range []string{"status_code", "http_status", "status"} {
				if status := responsesHTTPStatus(errorObject[key]); status != 0 {
					return status
				}
			}
		}
	}

	description := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v %s %s", openAIError.Code, openAIError.Type, openAIError.Message)))
	switch {
	case strings.Contains(description, "context_length"),
		strings.Contains(description, "context window"),
		strings.Contains(description, "invalid_request"),
		strings.Contains(description, "content_policy"),
		strings.Contains(description, "content filter"),
		strings.Contains(description, "prompt_blocked"):
		return http.StatusBadRequest
	case strings.Contains(description, "authentication"),
		strings.Contains(description, "invalid_api_key"),
		strings.Contains(description, "invalid token"):
		return http.StatusUnauthorized
	case strings.Contains(description, "rate_limit"),
		strings.Contains(description, "usage_limit"),
		strings.Contains(description, "quota"):
		return http.StatusTooManyRequests
	case strings.Contains(description, "timeout"):
		return http.StatusGatewayTimeout
	case strings.Contains(description, "overload"),
		strings.Contains(description, "unavailable"):
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

func responsesHTTPStatus(value any) int {
	var status int
	switch typed := value.(type) {
	case int:
		status = typed
	case int64:
		status = int(typed)
	case float64:
		status = int(typed)
	case string:
		status, _ = strconv.Atoi(strings.TrimSpace(typed))
	}
	if status < 100 || status > 599 {
		return 0
	}
	return status
}
