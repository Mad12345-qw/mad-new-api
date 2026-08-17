package relayconvert

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	codexresponses "github.com/QuantumNous/new-api/service/relayconvert/internal/codex_responses"
	"github.com/QuantumNous/new-api/setting/reasoning"
	"github.com/gin-gonic/gin"
)

type CodexResponsesWebsocketState = codexresponses.WebsocketState

var codexResponsesInternalMarker = newCodexResponsesInternalMarker()

func CodexResponsesInternalMarker() string {
	return codexResponsesInternalMarker
}

func newCodexResponsesInternalMarker() string {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("failed to initialize Codex compatibility marker: " + err.Error())
	}
	return hex.EncodeToString(value[:])
}

func NormalizeCodexResponsesRequest(rawJSON []byte) ([]byte, error) {
	return codexresponses.NormalizeOpenAIResponsesRequest(rawJSON)
}

func NormalizeCodexResponsesRequestForSelectedProvider(request dto.OpenAIResponsesRequest, apiType int) (dto.OpenAIResponsesRequest, error) {
	contract := codexresponses.ProviderContractOpenAICompat
	switch apiType {
	case appconstant.APITypeOpenAI:
		contract = codexresponses.ProviderContractOpenAI
	case appconstant.APITypeCodex:
		contract = codexresponses.ProviderContractCodex
	case appconstant.APITypeXai:
		contract = codexresponses.ProviderContractXAI
	case appconstant.APITypeAnthropic:
		contract = codexresponses.ProviderContractClaude
	case appconstant.APITypeGemini:
		contract = codexresponses.ProviderContractGemini
	case appconstant.APITypeDeepSeek:
		contract = codexresponses.ProviderContractDeepSeek
	case appconstant.APITypeMoonshot:
		contract = codexresponses.ProviderContractMoonshot
	}
	tools, toolChoice, err := codexresponses.EnsureOpenAINativeSearchToolFieldsForContract(request.Model, request.Tools, request.ToolChoice, contract)
	if err != nil {
		return request, err
	}
	request.Tools = tools
	request.ToolChoice = toolChoice
	rawJSON, err := common.Marshal(request)
	if err != nil {
		return request, err
	}
	normalized, err := codexresponses.NormalizeOpenAIResponsesRequestForContract(rawJSON, contract)
	if err != nil {
		return request, err
	}
	var result dto.OpenAIResponsesRequest
	if err = common.Unmarshal(normalized, &result); err != nil {
		return request, err
	}
	return result, nil
}

// NormalizeCodexOpenAIResponsesRawRequest applies the same OpenAI provider
// contract as NormalizeCodexResponsesRequestForSelectedProvider followed by
// the OpenAI adaptor, while keeping a large Responses input in raw JSON form.
// It is intentionally OpenAI-only; other provider paths retain their existing
// conversion pipeline.
func NormalizeCodexOpenAIResponsesRawRequest(rawJSON []byte, mappedModel string) ([]byte, string, error) {
	var request dto.OpenAIResponsesRequest
	if err := common.Unmarshal(rawJSON, &request); err != nil {
		return nil, "", fmt.Errorf("invalid OpenAI Responses request: %w", err)
	}
	request.Model = mappedModel

	tools, toolChoice, err := codexresponses.EnsureOpenAINativeSearchToolFieldsForContract(
		request.Model,
		request.Tools,
		request.ToolChoice,
		codexresponses.ProviderContractOpenAI,
	)
	if err != nil {
		return nil, "", err
	}
	request.Tools = tools
	request.ToolChoice = toolChoice

	effort, originModel := reasoning.ParseOpenAIReasoningEffortFromModelSuffix(request.Model)
	if effort != "" {
		if request.Reasoning == nil {
			request.Reasoning = &dto.Reasoning{Effort: effort}
		} else {
			request.Reasoning.Effort = effort
		}
		request.Model = originModel
	}
	if request.Reasoning != nil && request.Reasoning.Effort != "" {
		effort = request.Reasoning.Effort
	}

	prepared, err := common.Marshal(request)
	if err != nil {
		return nil, "", err
	}
	normalized, err := codexresponses.NormalizeOpenAIResponsesRequestForContract(
		prepared,
		codexresponses.ProviderContractOpenAI,
	)
	if err != nil {
		return nil, "", err
	}
	return normalized, effort, nil
}

func IsCodexResponsesInternalRequest(c *gin.Context) bool {
	return isCodexCompatibilityConversion(c)
}

func RestoreCodexResponsesPayload(originalRequest, rawJSON []byte) ([]byte, error) {
	return codexresponses.RestoreOpenAIResponsesPayload(originalRequest, rawJSON)
}
