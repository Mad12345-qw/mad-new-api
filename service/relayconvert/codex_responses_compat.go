package relayconvert

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	codexresponses "github.com/QuantumNous/new-api/service/relayconvert/internal/codex_responses"
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

func IsCodexResponsesInternalRequest(c *gin.Context) bool {
	return isCodexCompatibilityConversion(c)
}

func RestoreCodexResponsesPayload(originalRequest, rawJSON []byte) ([]byte, error) {
	return codexresponses.RestoreOpenAIResponsesPayload(originalRequest, rawJSON)
}
