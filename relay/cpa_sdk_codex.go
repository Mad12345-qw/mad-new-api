package relay

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
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
	defaultCPASDKDispatchURL = "http://cpa-sdk-host:18417/execute"
	cpaSDKMetadataLimit      = 1 << 20
)

var cpaSDKHopByHopHeaders = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"proxy-connection":    {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

var cpaSDKDispatchClient = newCPASDKDispatchClient()

type cpaSDKDispatchMeta struct {
	ChannelType int         `json:"channel_type"`
	ChannelID   int         `json:"channel_id"`
	UserID      int         `json:"user_id"`
	BaseURL     string      `json:"base_url"`
	APIKey      string      `json:"api_key"`
	Model       string      `json:"model"`
	Headers     http.Header `json:"headers,omitempty"`
	Stream      bool        `json:"stream"`
	Compact     bool        `json:"compact"`
	Source      string      `json:"source_format"`
	RequestPath string      `json:"request_path"`
}

func newCPASDKDispatchClient() *http.Client {
	headerTimeout := 60 * time.Second
	if raw := strings.TrimSpace(os.Getenv("MADAPI_CPA_RESPONSE_HEADER_TIMEOUT_SECONDS")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			headerTimeout = time.Duration(seconds) * time.Second
		}
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
	}}
}

// CPASDKSupportsImages reports whether the selected NewAPI channel maps to an
// official CPA executor that implements OpenAI-compatible image endpoints.
func CPASDKSupportsImages(channelType int) bool {
	apiType, _ := common.ChannelType2APIType(channelType)
	switch apiType {
	case appconstant.APITypeOpenAI,
		appconstant.APITypeXai,
		appconstant.APITypeCodex,
		appconstant.APITypeAdvancedCustom,
		appconstant.APITypeSub2API,
		appconstant.APITypeNewAPI:
		return true
	default:
		return false
	}
}

func CPASDKHelper(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	info.InitChannelMeta(c)
	payload, headers, source, err := cpaSDKRequest(c, info)
	if err != nil {
		return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	meta := cpaSDKDispatchMeta{
		ChannelType: info.ChannelType,
		ChannelID:   info.ChannelId,
		UserID:      info.UserId,
		BaseURL:     info.ChannelBaseUrl,
		APIKey:      info.ApiKey,
		Model:       info.UpstreamModelName,
		Headers:     headers,
		Stream:      info.IsStream,
		Compact:     info.RelayMode == relayconstant.RelayModeResponsesCompact,
		Source:      source,
		RequestPath: info.RequestURLPath,
	}
	response, err := dispatchToCPASDK(c, meta, payload)
	if err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	}
	if response.StatusCode != http.StatusOK {
		newAPIError := service.RelayErrorHandler(c.Request.Context(), response, false)
		service.ResetStatusCode(newAPIError, c.GetString("status_code_mapping"))
		return newAPIError
	}
	return cpaSDKWriteAndBill(c, info, response)
}

func dispatchToCPASDK(c *gin.Context, meta cpaSDKDispatchMeta, payload []byte) (*http.Response, error) {
	metadata, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal CPA SDK metadata: %w", err)
	}
	if len(metadata) > cpaSDKMetadataLimit {
		return nil, fmt.Errorf("CPA SDK metadata exceeds %d bytes", cpaSDKMetadataLimit)
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(metadata)))
	body := io.MultiReader(bytes.NewReader(prefix[:]), bytes.NewReader(metadata), bytes.NewReader(payload))
	url := strings.TrimSpace(os.Getenv(appconstant.MadAPICPASDKDispatchURLEnv))
	if url == "" {
		url = defaultCPASDKDispatchURL
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	request.ContentLength = int64(len(prefix) + len(metadata) + len(payload))
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set(appconstant.MadAPICPASDKDispatchHeader, strings.TrimSpace(os.Getenv(appconstant.MadAPICPASDKDispatchTokenEnv)))
	return cpaSDKDispatchClient.Do(request)
}

func cpaSDKRequest(c *gin.Context, info *relaycommon.RelayInfo) ([]byte, http.Header, string, error) {
	if info == nil || info.ChannelMeta == nil {
		return nil, nil, "", fmt.Errorf("selected channel metadata is unavailable")
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, nil, "", err
	}
	body, err := storage.Bytes()
	if err != nil {
		return nil, nil, "", err
	}
	body = bytes.Clone(body)
	source := "openai-response"
	if info.RelayMode == relayconstant.RelayModeImagesGenerations || info.RelayMode == relayconstant.RelayModeImagesEdits {
		source = "openai-image"
	} else {
		body, err = sjson.SetBytes(body, "model", info.UpstreamModelName)
		if err != nil {
			return nil, nil, "", fmt.Errorf("set upstream model: %w", err)
		}
		body, err = relaycommon.RemoveDisabledFields(body, info.ChannelOtherSettings, info.ChannelSetting.PassThroughBodyEnabled)
		if err != nil {
			return nil, nil, "", err
		}
	}
	if json.Valid(body) && len(info.ParamOverride) > 0 {
		body, err = relaycommon.ApplyParamOverrideWithRelayInfo(body, info)
		if err != nil {
			return nil, nil, "", err
		}
	}
	headers := make(http.Header)
	for name, values := range c.Request.Header {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "authorization", "x-api-key", "x-goog-api-key", "proxy-authorization", "content-length", "host":
			continue
		}
		for _, value := range values {
			headers.Add(name, value)
		}
	}
	overrides, err := channel.ResolveHeaderOverride(info, c)
	if err != nil {
		return nil, nil, "", err
	}
	for name, value := range overrides {
		headers.Set(name, value)
	}
	stripCPASDKHopByHopHeaders(headers)
	return body, headers, source, nil
}

func stripCPASDKHopByHopHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	connectionHeaders := append([]string(nil), headers.Values("Connection")...)
	for name := range cpaSDKHopByHopHeaders {
		headers.Del(name)
	}
	for _, value := range connectionHeaders {
		for _, name := range strings.Split(value, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				headers.Del(name)
			}
		}
	}
}

func cpaSDKWriteAndBill(c *gin.Context, info *relaycommon.RelayInfo, response *http.Response) *types.NewAPIError {
	var usage *dto.Usage
	var relayErr *types.NewAPIError
	if info.RelayMode == relayconstant.RelayModeImagesGenerations || info.RelayMode == relayconstant.RelayModeImagesEdits {
		usageAny, err := (&openai.Adaptor{}).DoResponse(c, response, info)
		relayErr = err
		if usageAny != nil {
			usage, _ = usageAny.(*dto.Usage)
		}
	} else if info.RelayMode == relayconstant.RelayModeResponsesCompact {
		usage, relayErr = openai.OaiResponsesCompactionHandler(c, response)
	} else if info.IsStream {
		usage, relayErr = openai.OaiResponsesStreamHandler(c, info, response)
	} else {
		usage, relayErr = openai.OaiResponsesHandler(c, info, response)
	}
	if relayErr != nil {
		service.ResetStatusCode(relayErr, c.GetString("status_code_mapping"))
		return relayErr
	}
	if usage == nil {
		usage = &dto.Usage{}
	}
	if info.RelayMode == relayconstant.RelayModeImagesGenerations || info.RelayMode == relayconstant.RelayModeImagesEdits {
		if usage.TotalTokens == 0 {
			usage.TotalTokens = 1
		}
		if usage.PromptTokens == 0 {
			usage.PromptTokens = 1
		}
		service.PostTextConsumeQuota(c, info, usage, nil)
		return nil
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
