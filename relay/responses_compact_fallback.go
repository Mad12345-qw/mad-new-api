package relay

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const nativeCompactInstruction = "Create a faithful compact state summary for continuing this coding session. Preserve user requirements, decisions, file paths, code changes, tool results, unresolved work, and safety constraints. Do not invent facts. Return only the compact summary text."

func prepareResponsesCompactFallback(req *dto.OpenAIResponsesRequest) {
	if req == nil {
		return
	}
	prefix, _ := common.Marshal(nativeCompactInstruction)
	if len(req.Instructions) == 0 || string(req.Instructions) == "null" {
		req.Instructions = prefix
		return
	}
	var existing string
	if err := common.Unmarshal(req.Instructions, &existing); err == nil && strings.TrimSpace(existing) != "" {
		combined, _ := common.Marshal(nativeCompactInstruction + "\n\nExisting instructions:\n" + existing)
		req.Instructions = combined
		return
	}
	req.Instructions = prefix
}

func responsesCompactFallbackHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid compact fallback response"), types.ErrorCodeBadResponse, http.StatusBadGateway)
	}
	defer service.CloseResponseBodyGracefully(resp)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusBadGateway)
	}

	responses, usage, err := compactFallbackToResponses(c, info, body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if usage == nil || usage.TotalTokens <= 0 {
		return nil, types.NewOpenAIError(fmt.Errorf("compact fallback upstream response omitted exact usage"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	summary := strings.TrimSpace(service.ExtractOutputTextFromResponses(responses))
	if summary == "" {
		return nil, types.NewOpenAIError(fmt.Errorf("compact fallback upstream response omitted summary text"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	output, err := common.Marshal([]map[string]any{{"type": "compaction_summary", "summary": summary}})
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	result := dto.OpenAIResponsesCompactionResponse{
		ID:        compactResponseID(responses.ID),
		Object:    "response.compaction",
		CreatedAt: responses.CreatedAt,
		Output:    output,
		Usage:     relayconvert.UsageFromChatUsage(usage),
	}
	if result.CreatedAt == 0 {
		result.CreatedAt = int(time.Now().Unix())
	}
	encoded, err := common.Marshal(result)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	service.IOCopyBytesGracefully(c, resp, encoded)
	return usage, nil
}

func compactFallbackToResponses(c *gin.Context, info *relaycommon.RelayInfo, body []byte) (*dto.OpenAIResponsesResponse, *dto.Usage, error) {
	var source any
	switch info.ApiType {
	case constant.APITypeAnthropic:
		var response dto.ClaudeResponse
		if err := common.Unmarshal(body, &response); err != nil {
			return nil, nil, err
		}
		if apiErr := response.GetClaudeError(); apiErr != nil && apiErr.Type != "" {
			return nil, nil, fmt.Errorf("Claude compact fallback: %s", apiErr.Message)
		}
		source = &response
	case constant.APITypeGemini:
		var response dto.GeminiChatResponse
		if err := common.Unmarshal(body, &response); err != nil {
			return nil, nil, err
		}
		source = &response
	case constant.APITypeXai:
		var response dto.OpenAIResponsesResponse
		if err := common.Unmarshal(body, &response); err != nil {
			return nil, nil, err
		}
		if apiErr := response.GetOpenAIError(); apiErr != nil && apiErr.Type != "" {
			return nil, nil, fmt.Errorf("xAI compact fallback: %s", apiErr.Message)
		}
		return &response, normalizeResponsesUsage(response.Usage), nil
	default:
		return nil, nil, fmt.Errorf("unsupported compact fallback api type %d", info.ApiType)
	}
	converted, err := relayconvert.ConvertResponse(c, info, types.RelayFormatOpenAIResponses, source)
	if err != nil {
		return nil, nil, err
	}
	responses, ok := converted.Value.(*dto.OpenAIResponsesResponse)
	if !ok {
		return nil, nil, fmt.Errorf("expected Responses compact fallback, got %T", converted.Value)
	}
	return responses, converted.Usage, nil
}

func normalizeResponsesUsage(source *dto.Usage) *dto.Usage {
	if source == nil {
		return nil
	}
	usage := *source
	usage.PromptTokens = source.InputTokens
	usage.CompletionTokens = source.OutputTokens
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if source.InputTokensDetails != nil {
		usage.PromptTokensDetails = *source.InputTokensDetails
	}
	usage.BillingUsage = dto.NewOpenAIResponsesBillingUsage(source)
	return &usage
}

func compactResponseID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Sprintf("cmp_%d", time.Now().UnixNano())
	}
	for _, prefix := range []string{"resp_", "msg_"} {
		if strings.HasPrefix(id, prefix) {
			return "cmp_" + strings.TrimPrefix(id, prefix)
		}
	}
	return "cmp_" + id
}
