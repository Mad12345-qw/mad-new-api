package relay

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/reasoning"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func ResponsesHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)

	var responsesReq *dto.OpenAIResponsesRequest
	switch req := info.Request.(type) {
	case *dto.OpenAIResponsesRequest:
		responsesReq = req
	case *dto.OpenAIResponsesCompactionRequest:
		// Only fields documented for POST /v1/responses/compact are forwarded:
		// model, input, instructions, previous_response_id, prompt_cache_key,
		// prompt_cache_options, prompt_cache_retention, service_tier.
		// Undocumented Codex-parity fields (tools, reasoning, text) are parsed
		// for client compatibility but intentionally not sent upstream.
		responsesReq = &dto.OpenAIResponsesRequest{
			Model:                req.Model,
			Input:                req.Input,
			Instructions:         req.Instructions,
			PreviousResponseID:   req.PreviousResponseID,
			ParallelToolCalls:    req.ParallelToolCalls,
			ServiceTier:          req.ServiceTier,
			PromptCacheKey:       req.PromptCacheKey,
			PromptCacheOptions:   req.PromptCacheOptions,
			PromptCacheRetention: req.PromptCacheRetention,
		}
	default:
		return types.NewErrorWithStatusCode(
			fmt.Errorf("invalid request type, expected dto.OpenAIResponsesRequest or dto.OpenAIResponsesCompactionRequest, got %T", info.Request),
			types.ErrorCodeInvalidRequest,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}

	var request *dto.OpenAIResponsesRequest
	if _, singleLayerCodex := common.GetContextKey(c, appconstant.ContextKeyRelayRequestPreprocessor); singleLayerCodex {
		request = cloneResponsesRequestForRelay(responsesReq)
	} else {
		var err error
		request, err = common.DeepCopy(responsesReq)
		if err != nil {
			return types.NewError(fmt.Errorf("failed to copy request to GeneralOpenAIRequest: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
	}

	err := helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}
	if info.RelayMode == relayconstant.RelayModeResponsesCompact {
		switch info.ApiType {
		case appconstant.APITypeOpenAI, appconstant.APITypeCodex:
		default:
			return types.NewErrorWithStatusCode(
				fmt.Errorf("unsupported endpoint %q for api type %d", "/v1/responses/compact", info.ApiType),
				types.ErrorCodeInvalidRequest,
				http.StatusBadRequest,
				types.ErrOptionWithSkipRetry(),
			)
		}
	}

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	var requestBody io.Reader
	_, singleLayerCodex := common.GetContextKey(c, appconstant.ContextKeyRelayRequestPreprocessor)
	if singleLayerCodex && canUseCodexNativeResponsesBody(info, responsesReq, request) {
		installCodexResponsesRestorer(c, func(payload []byte) ([]byte, error) { return payload, nil })
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return types.NewError(err, types.ErrorCodeReadRequestBodyFailed, types.ErrOptionWithSkipRetry())
		}
		info.UpstreamRequestBodySize = storage.Size()
		requestBody = common.ReaderOnly(storage)
	} else if !singleLayerCodex && (model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled) {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return types.NewError(err, types.ErrorCodeReadRequestBodyFailed, types.ErrOptionWithSkipRetry())
		}
		requestBody = common.ReaderOnly(storage)
	} else {
		if singleLayerCodex {
			restore, err := relayconvert.PrepareCodexResponsesRequest(request)
			if err != nil {
				return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
			}
			installCodexResponsesRestorer(c, restore)
		}
		convertedRequest, err := adaptor.ConvertOpenAIResponsesRequest(c, info, *request)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)
		jsonData, err := common.Marshal(convertedRequest)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}

		// remove disabled fields for OpenAI Responses API
		jsonData, err = relaycommon.RemoveDisabledFields(jsonData, info.ChannelOtherSettings, info.ChannelSetting.PassThroughBodyEnabled)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}

		// apply param override
		if len(info.ParamOverride) > 0 {
			jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
			if err != nil {
				return newAPIErrorFromParamOverride(err)
			}
		}

		logger.LogDebug(c, "requestBody: %s", jsonData)
		body, size, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		defer closer.Close()
		jsonData = nil
		info.UpstreamRequestBodySize = size
		requestBody = body
	}

	var httpResp *http.Response
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")

	if resp != nil {
		httpResp = resp.(*http.Response)

		if httpResp.StatusCode != http.StatusOK {
			newAPIError = service.RelayErrorHandler(c.Request.Context(), httpResp, false)
			// reset status code 重置状态码
			service.ResetStatusCode(newAPIError, statusCodeMappingStr)
			return newAPIError
		}
	}

	usage, newAPIError := adaptor.DoResponse(c, httpResp, info)
	if newAPIError != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}

	usageDto := usage.(*dto.Usage)
	if info.RelayMode == relayconstant.RelayModeResponsesCompact {
		originModelName := info.OriginModelName
		originPriceData := info.PriceData
		_, err := helper.ModelPriceHelper(c, info, info.GetEstimatePromptTokens(), &types.TokenCountMeta{})
		if err != nil {
			info.OriginModelName = originModelName
			info.PriceData = originPriceData
			return types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithSkipRetry(), types.ErrOptionWithStatusCode(http.StatusBadRequest))
		}
		service.PostTextConsumeQuota(c, info, usageDto, nil)

		info.OriginModelName = originModelName
		info.PriceData = originPriceData
		return nil
	}

	if strings.HasPrefix(info.OriginModelName, "gpt-4o-audio") {
		service.PostAudioConsumeQuota(c, info, usageDto, "")
	} else {
		service.PostTextConsumeQuota(c, info, usageDto, nil)
	}
	return nil
}

func cloneResponsesRequestForRelay(source *dto.OpenAIResponsesRequest) *dto.OpenAIResponsesRequest {
	// Adaptors receive the request by value. RawMessage fields are immutable;
	// pointer values get a tiny independent copy so every NewAPI retry starts
	// from the same parsed request without a recursive DeepCopy.
	cloned := *source
	if source.Reasoning != nil {
		reasoning := *source.Reasoning
		cloned.Reasoning = &reasoning
	}
	if source.MaxOutputTokens != nil {
		value := *source.MaxOutputTokens
		cloned.MaxOutputTokens = &value
	}
	if source.TopLogProbs != nil {
		value := *source.TopLogProbs
		cloned.TopLogProbs = &value
	}
	if source.Stream != nil {
		value := *source.Stream
		cloned.Stream = &value
	}
	if source.Temperature != nil {
		value := *source.Temperature
		cloned.Temperature = &value
	}
	if source.TopP != nil {
		value := *source.TopP
		cloned.TopP = &value
	}
	if source.MaxToolCalls != nil {
		value := *source.MaxToolCalls
		cloned.MaxToolCalls = &value
	}
	if source.StreamOptions != nil {
		value := *source.StreamOptions
		cloned.StreamOptions = &value
	}
	return &cloned
}

func installCodexResponsesRestorer(c *gin.Context, restore func([]byte) ([]byte, error)) {
	value, ok := c.Get(relayconvert.CodexResponsesRestoreInstallerContextKey)
	if !ok {
		return
	}
	installer, ok := value.(relayconvert.CodexResponsesRestoreInstaller)
	if ok {
		installer(restore)
	}
}

func canUseCodexNativeResponsesBody(info *relaycommon.RelayInfo, original, request *dto.OpenAIResponsesRequest) bool {
	if info == nil || original == nil || request == nil || info.RelayMode != relayconstant.RelayModeResponses {
		return false
	}
	switch info.ApiType {
	case appconstant.APITypeOpenAI, appconstant.APITypeOpenRouter, appconstant.APITypeXinference:
	default:
		return false
	}
	if info.IsModelMapped || request.Model != original.Model {
		return false
	}
	if effort, _ := reasoning.ParseOpenAIReasoningEffortFromModelSuffix(request.Model); effort != "" {
		return false
	}
	if relayconvert.CodexResponsesRequestNeedsNormalization(request) || len(info.ParamOverride) > 0 {
		return false
	}
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled {
		return true
	}
	settings := info.ChannelOtherSettings
	if (!settings.AllowServiceTier && request.ServiceTier != "") ||
		(settings.DisableStore && len(request.Store) > 0) ||
		(!settings.AllowSafetyIdentifier && len(request.SafetyIdentifier) > 0) ||
		(!settings.AllowIncludeObfuscation && request.StreamOptions != nil && request.StreamOptions.IncludeObfuscation) {
		return false
	}
	return true
}
