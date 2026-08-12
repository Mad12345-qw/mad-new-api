package relay

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/sjson"
)

const (
	defaultCPAHandlerURL  = "http://cpa-official-gateway:18417/execute"
	cpaExecuteTokenHeader = "X-MadAPI-CPA-Execute-Token"
	cpaUsageTrailer       = "X-MadAPI-CPA-Usage"
	cpaExecuteMetaLimit   = 1 << 20
)

type cpaHandlerMeta struct {
	Provider      string      `json:"provider"`
	ChannelID     int         `json:"channel_id"`
	UserID        int         `json:"user_id"`
	BaseURL       string      `json:"base_url"`
	APIKey        string      `json:"api_key"`
	Model         string      `json:"model"`
	OriginalModel string      `json:"original_model"`
	Headers       http.Header `json:"headers,omitempty"`
	RequestPath   string      `json:"request_path"`
}

var cpaHandlerClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	ForceAttemptHTTP2:     false,
	MaxIdleConns:          256,
	MaxIdleConnsPerHost:   256,
	IdleConnTimeout:       90 * time.Second,
	ResponseHeaderTimeout: 60 * time.Second,
}}

func CPAHandlerHelper(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	info.InitChannelMeta(c)
	payload, headers, err := cpaHandlerRequest(c, info)
	if err != nil {
		return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	provider, err := cpaHandlerProvider(info.ApiType)
	if err != nil {
		return types.NewError(err, types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	path := "/v1/responses"
	if info.RelayMode == relayconstant.RelayModeResponsesCompact {
		path = "/v1/responses/compact"
	}
	meta := cpaHandlerMeta{
		Provider: provider, ChannelID: info.ChannelId, UserID: info.UserId,
		BaseURL: cpaHandlerBaseURL(info.ChannelBaseUrl, provider), APIKey: info.ApiKey,
		Model: info.UpstreamModelName, OriginalModel: info.OriginModelName,
		Headers: headers, RequestPath: path,
	}
	response, err := dispatchToCPAHandler(c, meta, payload)
	if err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	}
	if response.StatusCode != http.StatusOK {
		newAPIError := service.RelayErrorHandler(c.Request.Context(), response, false)
		service.ResetStatusCode(newAPIError, c.GetString("status_code_mapping"))
		return newAPIError
	}
	return cpaHandlerWriteAndBill(c, info, response)
}

func cpaHandlerRequest(c *gin.Context, info *relaycommon.RelayInfo) ([]byte, http.Header, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, nil, err
	}
	body, err := storage.Bytes()
	if err != nil {
		return nil, nil, err
	}
	body, err = sjson.SetBytes(bytes.Clone(body), "model", info.UpstreamModelName)
	if err != nil {
		return nil, nil, fmt.Errorf("set upstream model: %w", err)
	}
	if len(info.ParamOverride) > 0 {
		body, err = relaycommon.ApplyParamOverrideWithRelayInfo(body, info)
		if err != nil {
			return nil, nil, err
		}
	}
	headers := make(http.Header)
	for name, values := range c.Request.Header {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "authorization", "x-api-key", "x-goog-api-key", "host", "content-length", "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
			continue
		}
		for _, value := range values {
			headers.Add(name, value)
		}
	}
	overrides, err := channel.ResolveHeaderOverride(info, c)
	if err != nil {
		return nil, nil, err
	}
	for name, value := range overrides {
		headers.Set(name, value)
	}
	return body, headers, nil
}

func dispatchToCPAHandler(c *gin.Context, meta cpaHandlerMeta, payload []byte) (*http.Response, error) {
	metadata, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal CPA handler metadata: %w", err)
	}
	if len(metadata) > cpaExecuteMetaLimit {
		return nil, fmt.Errorf("CPA handler metadata exceeds %d bytes", cpaExecuteMetaLimit)
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(metadata)))
	body := io.MultiReader(bytes.NewReader(prefix[:]), bytes.NewReader(metadata), bytes.NewReader(payload))
	url := strings.TrimSpace(os.Getenv("MADAPI_CPA_HANDLER_URL"))
	if url == "" {
		url = defaultCPAHandlerURL
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	request.ContentLength = int64(len(prefix) + len(metadata) + len(payload))
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set(cpaExecuteTokenHeader, strings.TrimSpace(os.Getenv("MADAPI_CPA_CONTROL_TOKEN")))
	return cpaHandlerClient.Do(request)
}

func cpaHandlerWriteAndBill(c *gin.Context, info *relaycommon.RelayInfo, response *http.Response) *types.NewAPIError {
	var exactUsage *dto.Usage
	if !info.IsStream {
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			return types.NewError(err, types.ErrorCodeReadResponseBodyFailed, types.ErrOptionWithSkipRetry())
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		exactUsage = &dto.Usage{}
		found, err := applyCPAUsageTrailer(response, exactUsage)
		if err != nil {
			return types.NewError(err, types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
		}
		if !found {
			return types.NewError(errors.New("CPA response omitted upstream usage"), types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
		}
	}
	var usage *dto.Usage
	var relayErr *types.NewAPIError
	streamSettled := false
	if info.RelayMode == relayconstant.RelayModeResponsesCompact {
		usage, relayErr = openai.OaiResponsesCompactionHandler(c, response)
	} else if info.IsStream {
		usage, relayErr = openai.OaiResponsesStreamHandlerExactUsage(c, info, response, func(exact *dto.Usage) error {
			if err := normalizeCPAHandlerUsage(exact); err != nil {
				return err
			}
			service.PostTextConsumeQuota(c, info, exact, nil)
			streamSettled = true
			return nil
		})
	} else {
		usage, relayErr = openai.OaiResponsesHandler(c, info, response)
	}
	if relayErr != nil && info.IsStream {
		if streamSettled {
			return nil
		}
		usage = &dto.Usage{}
		if found, trailerErr := applyCPAUsageTrailer(response, usage); trailerErr != nil {
			return types.NewError(trailerErr, types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
		} else if found {
			relayErr = nil
		}
	}
	if relayErr != nil {
		service.ResetStatusCode(relayErr, c.GetString("status_code_mapping"))
		return relayErr
	}
	if streamSettled {
		return nil
	}
	if usage == nil {
		usage = &dto.Usage{}
	}
	if !info.IsStream {
		*usage = *exactUsage
	}
	if err := normalizeCPAHandlerUsage(usage); err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
	}
	if info.RelayMode == relayconstant.RelayModeResponsesCompact {
		originModelName, originPriceData := info.OriginModelName, info.PriceData
		if _, err := helper.ModelPriceHelper(c, info, info.GetEstimatePromptTokens(), &types.TokenCountMeta{}); err != nil {
			info.OriginModelName, info.PriceData = originModelName, originPriceData
			return types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithSkipRetry(), types.ErrOptionWithStatusCode(http.StatusBadRequest))
		}
		service.PostTextConsumeQuota(c, info, usage, nil)
		info.OriginModelName, info.PriceData = originModelName, originPriceData
		return nil
	}
	service.PostTextConsumeQuota(c, info, usage, nil)
	return nil
}

func normalizeCPAHandlerUsage(usage *dto.Usage) error {
	if usage == nil {
		return errors.New("CPA response omitted upstream usage")
	}
	if !service.ValidEffectiveUsage(usage) {
		return errors.New("CPA response omitted exact upstream input/output usage")
	}
	return nil
}

type cpaExactUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	ReasoningTokens     int `json:"reasoning_tokens"`
	CachedTokens        int `json:"cached_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens"`
	TotalTokens         int `json:"total_tokens"`
}

func applyCPAUsageTrailer(response *http.Response, usage *dto.Usage) (bool, error) {
	if response == nil || usage == nil {
		return false, nil
	}
	raw := strings.TrimSpace(response.Trailer.Get(cpaUsageTrailer))
	if raw == "" {
		return false, nil
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil {
		return false, fmt.Errorf("decode CPA usage trailer: %w", err)
	}
	var exact cpaExactUsage
	if err = json.Unmarshal(decoded, &exact); err != nil {
		return false, fmt.Errorf("parse CPA usage trailer: %w", err)
	}
	usage.BillingUsage = nil
	usage.PromptTokens = exact.InputTokens
	usage.CompletionTokens = exact.OutputTokens
	usage.TotalTokens = exact.TotalTokens
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	usage.PromptTokensDetails.CachedTokens = max(exact.CachedTokens, exact.CacheReadTokens)
	usage.PromptTokensDetails.CachedCreationTokens = exact.CacheCreationTokens
	usage.CompletionTokenDetails.ReasoningTokens = exact.ReasoningTokens
	usage.UsageSource = "cpa-official-upstream"
	return true, nil
}

func cpaHandlerProvider(apiType int) (string, error) {
	switch apiType {
	case constant.APITypeCodex:
		return "codex", nil
	case constant.APITypeOpenAI, constant.APITypeSub2API, constant.APITypeNewAPI:
		return "openai-compatibility", nil
	case constant.APITypeAnthropic:
		return "claude", nil
	case constant.APITypeGemini:
		return "gemini", nil
	case constant.APITypeXai:
		return "xai", nil
	default:
		return "", fmt.Errorf("selected NewAPI API type %d has no verified CPA executor mapping", apiType)
	}
}

func cpaHandlerBaseURL(raw, provider string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(raw), "/")
	if provider == "openai-compatibility" && baseURL != "" && !strings.HasSuffix(strings.ToLower(baseURL), "/v1") {
		baseURL += "/v1"
	}
	return baseURL
}
