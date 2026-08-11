package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type cpaControlAuthRecord struct {
	ID         string            `json:"id"`
	Provider   string            `json:"provider"`
	Status     string            `json:"status"`
	Attributes map[string]string `json:"attributes"`
	Metadata   map[string]any    `json:"metadata,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

type cpaControlDispatchResponse struct {
	Ticket        string               `json:"ticket"`
	Model         string               `json:"model"`
	Provider      string               `json:"provider"`
	AuthIndex     string               `json:"auth_index"`
	UserAPIKey    string               `json:"user_api_key"`
	OriginalAlias string               `json:"original_alias,omitempty"`
	ForceMapping  bool                 `json:"force_mapping,omitempty"`
	Auth          cpaControlAuthRecord `json:"auth"`
}

type cpaControlSettleRequest struct {
	Ticket  string           `json:"ticket"`
	Outcome string           `json:"outcome"`
	Usage   service.CPAUsage `json:"usage"`
}

type cpaCodexOAuthKey struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id"`
}

func CPAControlAuth(c *gin.Context) {
	userID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	tokenID := common.GetContextKeyInt(c, constant.ContextKeyTokenId)
	if userID <= 0 || tokenID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid NewAPI token context"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"principal": fmt.Sprintf("user:%d:token:%d", userID, tokenID),
		"user_id":   userID,
		"token_id":  tokenID,
		"group":     common.GetContextKeyString(c, constant.ContextKeyUsingGroup),
	})
}

func CPAControlDispatch(c *gin.Context) {
	relayFormat, err := cpaRelayFormat(c.Request.URL.Path)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	request, err := helper.GetAndValidateRequest(c, relayFormat)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	info, err := relaycommon.GenRelayInfo(c, relayFormat, request, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	info.InitChannelMeta(c)

	meta := request.GetTokenCountMeta()
	estimated, err := service.EstimateRequestToken(c, meta, info)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	info.SetEstimatePromptTokens(estimated)
	priceData, err := helper.ModelPriceHelper(c, info, estimated, meta)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !priceData.FreeModel {
		if apiErr := service.PreConsumeBilling(c, priceData.QuotaToPreConsume, info); apiErr != nil {
			c.JSON(apiErr.StatusCode, gin.H{"error": apiErr.ToOpenAIError()})
			return
		}
	}

	response, err := buildCPAControlDispatch(c, info)
	if err != nil {
		if info.Billing != nil {
			info.Billing.Refund(c)
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	service.RegisterCPAPending(c, response.Ticket, response.Auth.ID, info)
	c.JSON(http.StatusOK, response)
}

func CPAControlSettle(c *gin.Context) {
	var request cpaControlSettleRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	request.Ticket = strings.TrimSpace(request.Ticket)
	if request.Ticket == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ticket is required"})
		return
	}

	var err error
	if strings.EqualFold(strings.TrimSpace(request.Outcome), "succeeded") {
		err = service.SettleCPADispatch(c, request.Ticket, request.Usage)
	} else {
		err = service.RefundCPADispatch(c, request.Ticket)
	}
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "ticket": request.Ticket})
}

func cpaRelayFormat(path string) (types.RelayFormat, error) {
	switch path {
	case "/v1/responses":
		return types.RelayFormatOpenAIResponses, nil
	case "/v1/responses/compact":
		return types.RelayFormatOpenAIResponsesCompaction, nil
	case "/v1/images/generations", "/v1/images/edits":
		return types.RelayFormatOpenAIImage, nil
	default:
		return "", fmt.Errorf("unsupported CPA path %q", path)
	}
}

func buildCPAControlDispatch(c *gin.Context, info *relaycommon.RelayInfo) (*cpaControlDispatchResponse, error) {
	if info == nil || info.ChannelMeta == nil {
		return nil, fmt.Errorf("NewAPI did not select a channel")
	}
	provider, err := cpaProviderForAPIType(info.ApiType)
	if err != nil {
		return nil, err
	}
	baseURL := cpaNormalizedBaseURL(info.ChannelBaseUrl, provider)
	attributes := map[string]string{
		"api_key":      info.ApiKey,
		"auth_kind":    "api-key",
		"runtime_only": "true",
	}
	if baseURL != "" {
		attributes["base_url"] = baseURL
	}
	for name, rawValue := range info.HeadersOverride {
		value := strings.TrimSpace(fmt.Sprint(rawValue))
		if strings.TrimSpace(name) != "" && value != "" {
			attributes["header:"+name] = value
		}
	}
	var metadata map[string]any
	if provider == "xai" {
		attributes["using_api"] = "true"
	}
	if provider == "codex" && strings.HasPrefix(strings.TrimSpace(info.ApiKey), "{") {
		var key cpaCodexOAuthKey
		if err := json.Unmarshal([]byte(info.ApiKey), &key); err != nil {
			return nil, fmt.Errorf("invalid Codex OAuth channel credential")
		}
		key.AccessToken = strings.TrimSpace(key.AccessToken)
		key.AccountID = strings.TrimSpace(key.AccountID)
		if key.AccessToken == "" || key.AccountID == "" {
			return nil, fmt.Errorf("incomplete Codex OAuth channel credential")
		}
		delete(attributes, "api_key")
		attributes["auth_kind"] = "oauth"
		metadata = map[string]any{"access_token": key.AccessToken, "account_id": key.AccountID}
	}

	authID := cpaStableAuthID(info.ChannelId, provider, baseURL, info.ApiKey)
	ticket := uuid.NewString()
	now := time.Now().UTC()
	return &cpaControlDispatchResponse{
		Ticket:        ticket,
		Model:         info.UpstreamModelName,
		Provider:      provider,
		AuthIndex:     strconv.Itoa(info.ChannelId),
		UserAPIKey:    ticket,
		OriginalAlias: info.OriginModelName,
		ForceMapping:  info.UpstreamModelName != info.OriginModelName,
		Auth: cpaControlAuthRecord{
			ID: authID, Provider: provider, Status: "active",
			Attributes: attributes, Metadata: metadata, CreatedAt: now, UpdatedAt: now,
		},
	}, nil
}

func cpaProviderForAPIType(apiType int) (string, error) {
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

func cpaNormalizedBaseURL(raw, provider string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(raw), "/")
	if baseURL == "" {
		return ""
	}
	if provider == "openai-compatibility" && !strings.HasSuffix(strings.ToLower(baseURL), "/v1") {
		baseURL += "/v1"
	}
	return baseURL
}

func cpaStableAuthID(channelID int, provider, baseURL, apiKey string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + strconv.Itoa(channelID) + "\x00" + baseURL + "\x00" + apiKey))
	return fmt.Sprintf("madapi-channel-%d-%s-%s", channelID, provider, hex.EncodeToString(sum[:8]))
}
