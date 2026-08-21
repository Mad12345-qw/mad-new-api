package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/codexclientmodels"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
	"github.com/tidwall/gjson"
)

const (
	defaultCodexInternalBaseURL   = "http://127.0.0.1:3000/v1"
	codexNativeCompactInstruction = "Create a faithful compact state summary for continuing this coding session. Preserve user requirements, decisions, file paths, code changes, tool results, unresolved work, and safety constraints. Do not invent facts. Return only the compact summary text."
)

var (
	codexInternalBaseURL     = defaultCodexInternalBaseURL
	codexTextPricingProvider = codexTextPricingModels
	codexRelayExecutor       = Relay
	codexInternalHTTPClient  = &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     false,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 360 * time.Second,
		},
	}
)

type codexOpenAIModelsResponse struct {
	Data []dto.OpenAIModels `json:"data"`
}

type codexModelsResponse struct {
	Models []map[string]any `json:"models"`
}

func CodexListModels(c *gin.Context) {
	req, err := newCodexInternalRequest(c, http.MethodGet, "/models", nil)
	if err != nil {
		codexCompatibilityError(c, http.StatusInternalServerError, err)
		return
	}

	resp, err := codexInternalHTTPClient.Do(req)
	if err != nil {
		codexCompatibilityError(c, http.StatusBadGateway, fmt.Errorf("model catalog request failed: %w", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		proxyCodexErrorResponse(c, resp)
		return
	}

	var upstream codexOpenAIModelsResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	if err = decoder.Decode(&upstream); err != nil {
		codexCompatibilityError(c, http.StatusBadGateway, fmt.Errorf("invalid model catalog response: %w", err))
		return
	}

	textPricing := codexTextPricingProvider()
	models := make([]dto.OpenAIModels, 0, len(upstream.Data))
	for _, candidate := range upstream.Data {
		if isCodexConversationModel(candidate, textPricing) {
			models = append(models, candidate)
		}
	}
	sort.SliceStable(models, func(i, j int) bool {
		return strings.ToLower(models[i].Id) < strings.ToLower(models[j].Id)
	})

	availableModels := make([]map[string]any, 0, len(models))
	metadata := make(map[string]*codexclientmodels.ModelInfo, len(models))
	for _, candidate := range models {
		availableModels = append(availableModels, codexAvailableModel(candidate))
		metadata[candidate.Id] = codexClientModelInfo(candidate)
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, codexclientmodels.BuildResponse(
		availableModels,
		nil,
		false,
		func(id string) *codexclientmodels.ModelInfo { return metadata[id] },
		func(id string) bool { return codexNativeSearchModels[strings.ToLower(strings.TrimSpace(id))] },
	))
}

func codexAvailableModel(candidate dto.OpenAIModels) map[string]any {
	model := map[string]any{
		"id":           candidate.Id,
		"display_name": candidate.Id,
		"description":  "Available through MadAPI: " + candidate.Id,
	}
	if alias, ok := codexCapabilityAliases[candidate.Id]; ok {
		model["template_id"] = alias.BaseID
	}
	if info := codexCatalogModel(candidate); info != nil {
		if value := strings.TrimSpace(info.DisplayName); value != "" {
			model["display_name"] = value
		}
		if value := strings.TrimSpace(info.Description); value != "" {
			model["description"] = value
		}
		if contextLength := info.effectiveContextLength(); contextLength > 0 {
			model["context_length"] = contextLength
		}
	}
	return model
}

func codexClientModelInfo(candidate dto.OpenAIModels) *codexclientmodels.ModelInfo {
	info := codexCatalogModel(candidate)
	if info == nil {
		return nil
	}
	result := &codexclientmodels.ModelInfo{
		DisplayName:              info.DisplayName,
		Description:              info.Description,
		ContextLength:            info.effectiveContextLength(),
		SupportedInputModalities: append([]string(nil), info.SupportedInputModalities...),
	}
	if info.Thinking != nil {
		result.Thinking = &codexclientmodels.ThinkingSupport{Levels: append([]string(nil), info.Thinking.Levels...)}
	}
	return result
}

func CodexResponses(c *gin.Context) {
	state := &codexSingleLayerStreamState{}
	c.Set(relayconvert.CodexResponsesRestoreInstallerContextKey, relayconvert.CodexResponsesRestoreInstaller(func(restore func([]byte) ([]byte, error)) {
		common.SetContextKey(c, constant.ContextKeyResponsesPayloadTransformer, restore)
		common.SetContextKey(c, constant.ContextKeyResponsesStreamEventTransformer, func(eventType string, payload []byte) (string, []byte, bool, error) {
			return state.transformEvent(restore, eventType, payload)
		})
	}))
	common.SetContextKey(c, constant.ContextKeyRelayRequestPreprocessor, func(request dto.Request) error {
		responsesReq, ok := request.(*dto.OpenAIResponsesRequest)
		if !ok {
			return fmt.Errorf("invalid Codex Responses request type %T", request)
		}
		state.setStream(lo.FromPtrOr(responsesReq.Stream, false))
		common.SetContextKey(c, constant.ContextKeyRelayErrorResponseHandler, func(relayErr *types.NewAPIError) bool {
			return state.writeRelayError(c, relayErr)
		})
		if !isCodexConversationModel(dto.OpenAIModels{Id: responsesReq.Model}, codexTextPricingProvider()) {
			return fmt.Errorf("model %q is not a Codex conversation model", responsesReq.Model)
		}
		return nil
	})

	originalURL := c.Request.URL
	relayURL := *originalURL
	relayURL.Path = "/v1/responses"
	relayURL.RawPath = ""
	c.Request.URL = &relayURL
	defer func() { c.Request.URL = originalURL }()

	codexRelayExecutor(c, types.RelayFormatOpenAIResponses)
	state.finish(c)
}

// CodexCockpitResponses preserves the isolated Cockpit model-shell rewrite.
// It is not used by the standard /codex/v1/responses path.
func CodexCockpitResponses(c *gin.Context) {
	CodexResponses(c)
}

type codexSingleLayerStreamState struct {
	mu         sync.Mutex
	stream     bool
	started    bool
	terminal   bool
	responseID string
}

func (state *codexSingleLayerStreamState) setStream(stream bool) {
	state.mu.Lock()
	state.stream = stream
	state.mu.Unlock()
}

func (state *codexSingleLayerStreamState) transformEvent(
	restore func([]byte) ([]byte, error),
	eventType string,
	payload []byte,
) (string, []byte, bool, error) {
	restored, err := restore(payload)
	if err != nil {
		return "", nil, false, err
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if value := gjson.GetBytes(restored, "type").String(); value != "" {
		eventType = value
	}
	if id := gjson.GetBytes(restored, "response.id").String(); id != "" {
		state.responseID = id
	}
	if id := gjson.GetBytes(restored, "id").String(); id != "" && state.responseID == "" {
		state.responseID = id
	}
	if eventType == "error" || eventType == "response.error" {
		state.terminal = true
		return "response.failed", buildCodexTerminalPayload("response.failed", state.responseID, codexEventErrorMessage(restored), "upstream_error"), false, nil
	}
	if strings.HasPrefix(eventType, "response.") {
		state.started = true
	}
	if eventType == "response.completed" || eventType == "response.failed" || eventType == "response.incomplete" {
		state.terminal = true
	}
	return eventType, restored, false, nil
}

func (state *codexSingleLayerStreamState) writeRelayError(c *gin.Context, relayErr *types.NewAPIError) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.stream {
		return false
	}
	if state.terminal {
		return true
	}
	state.terminal = true
	helper.SetEventStreamHeaders(c)
	c.Status(http.StatusOK)
	writeCodexTerminalEvent(c, "response.failed", state.responseID, relayErr.Error(), "upstream_error")
	return true
}

func (state *codexSingleLayerStreamState) finish(c *gin.Context) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.stream || state.terminal {
		return
	}
	eventType := "response.failed"
	code := "empty_stream"
	message := "upstream stream ended before producing a complete response"
	if state.started {
		eventType = "response.incomplete"
		code = "stream_ended"
		message = "upstream stream ended before its terminal event"
	}
	state.terminal = true
	helper.SetEventStreamHeaders(c)
	c.Status(http.StatusOK)
	writeCodexTerminalEvent(c, eventType, state.responseID, message, code)
}

func CodexResponsesCompact(c *gin.Context) {
	originalBody, err := readCodexRequestBody(c)
	if err != nil {
		codexCompatibilityError(c, http.StatusBadRequest, err)
		return
	}
	originalBody, err = rewriteCodexCockpitRequestBody(c, originalBody)
	if err != nil {
		codexCompatibilityError(c, http.StatusBadRequest, err)
		return
	}
	var compactReq dto.OpenAIResponsesCompactionRequest
	if err = common.Unmarshal(originalBody, &compactReq); err != nil || strings.TrimSpace(compactReq.Model) == "" {
		codexCompatibilityError(c, http.StatusBadRequest, fmt.Errorf("invalid Responses compact request"))
		return
	}
	if !isCodexConversationModel(dto.OpenAIModels{Id: compactReq.Model}, codexTextPricingProvider()) {
		codexCompatibilityError(c, http.StatusBadRequest, fmt.Errorf("model %q is not a Codex conversation model", compactReq.Model))
		return
	}

	body, err := buildCodexNativeCompactRequest(compactReq)
	if err != nil {
		codexCompatibilityError(c, http.StatusBadRequest, err)
		return
	}
	common.CleanupBodyStorage(c)
	c.Set(common.KeyRequestBody, body)
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	common.SetContextKey(c, constant.ContextKeyRelayRequestPreprocessor, func(dto.Request) error { return nil })
	common.SetContextKey(c, constant.ContextKeyResponsesPayloadTransformer, buildCodexNativeCompactResponsePayload)

	originalURL := c.Request.URL
	relayURL := *originalURL
	relayURL.Path = "/v1/responses"
	relayURL.RawPath = ""
	c.Request.URL = &relayURL
	defer func() { c.Request.URL = originalURL }()
	codexRelayExecutor(c, types.RelayFormatOpenAIResponses)
}

func buildCodexNativeCompactRequest(compactReq dto.OpenAIResponsesCompactionRequest) ([]byte, error) {
	instructions := codexNativeCompactInstruction
	var existing string
	if len(compactReq.Instructions) > 0 && string(compactReq.Instructions) != "null" {
		if err := common.Unmarshal(compactReq.Instructions, &existing); err == nil && strings.TrimSpace(existing) != "" {
			instructions += "\n\nExisting instructions:\n" + existing
		}
	}
	encodedInstructions, err := common.Marshal(instructions)
	if err != nil {
		return nil, err
	}
	stream := false
	request := dto.OpenAIResponsesRequest{
		Model:                compactReq.Model,
		Input:                compactReq.Input,
		Instructions:         encodedInstructions,
		PreviousResponseID:   compactReq.PreviousResponseID,
		ParallelToolCalls:    compactReq.ParallelToolCalls,
		ServiceTier:          compactReq.ServiceTier,
		PromptCacheKey:       compactReq.PromptCacheKey,
		PromptCacheOptions:   compactReq.PromptCacheOptions,
		PromptCacheRetention: compactReq.PromptCacheRetention,
		Stream:               &stream,
	}
	return common.Marshal(request)
}

func buildCodexNativeCompactResponsePayload(body []byte) ([]byte, error) {
	var responses dto.OpenAIResponsesResponse
	if err := common.Unmarshal(body, &responses); err != nil {
		return nil, fmt.Errorf("invalid native compact response: %w", err)
	}
	summary := strings.TrimSpace(service.ExtractOutputTextFromResponses(&responses))
	if summary == "" {
		return nil, fmt.Errorf("native compact response omitted summary text")
	}
	output, err := common.Marshal([]map[string]any{{"type": "compaction_summary", "summary": summary}})
	if err != nil {
		return nil, err
	}
	result := dto.OpenAIResponsesCompactionResponse{
		ID:        codexCompactResponseID(responses.ID),
		Object:    "response.compaction",
		CreatedAt: responses.CreatedAt,
		Output:    output,
		Usage:     responses.Usage,
	}
	if result.CreatedAt == 0 {
		result.CreatedAt = int(time.Now().Unix())
	}
	encoded, err := common.Marshal(result)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func codexCompactResponseID(id string) string {
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

func codexEventErrorMessage(payload []byte) string {
	if !json.Valid(payload) {
		return strings.TrimSpace(string(payload))
	}
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return strings.TrimSpace(string(payload))
	}
	if message, ok := value["message"].(string); ok && message != "" {
		return message
	}
	if errValue, ok := value["error"].(map[string]any); ok {
		if message, ok := errValue["message"].(string); ok && message != "" {
			return message
		}
	}
	return "upstream stream failed"
}

func writeCodexTerminalEvent(c *gin.Context, eventType string, responseID string, message string, code string) {
	encoded := buildCodexTerminalPayload(eventType, responseID, message, code)
	_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", eventType, encoded)
	c.Writer.Flush()
}

func buildCodexTerminalPayload(eventType string, responseID string, message string, code string) []byte {
	if responseID == "" {
		responseID = fmt.Sprintf("resp_madapi_%d", time.Now().UnixNano())
	}
	status := "failed"
	if eventType == "response.incomplete" {
		status = "incomplete"
	}
	payload := map[string]any{
		"type": eventType,
		"response": map[string]any{
			"id":     responseID,
			"object": "response",
			"status": status,
			"error": map[string]any{
				"code":    code,
				"message": message,
			},
		},
	}
	encoded, _ := json.Marshal(payload)
	return encoded
}

func readCodexRequestBody(c *gin.Context) ([]byte, error) {
	body, err := common.GetRequestBody(c)
	if err != nil {
		return nil, err
	}
	if _, err = body.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	reader, ok := body.(io.Reader)
	if !ok {
		return nil, fmt.Errorf("request body is not readable")
	}
	raw, err := io.ReadAll(reader)
	if _, seekErr := body.Seek(0, io.SeekStart); err == nil && seekErr != nil {
		err = seekErr
	}
	return raw, err
}

func newCodexInternalRequest(c *gin.Context, method string, path string, body io.Reader) (*http.Request, error) {
	base := strings.TrimRight(codexInternalBaseURL, "/")
	req, err := http.NewRequestWithContext(c.Request.Context(), method, base+path, body)
	if err != nil {
		return nil, err
	}
	if authorization := c.GetHeader("Authorization"); authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if userAgent := c.GetHeader("User-Agent"); userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	if clientIP := c.ClientIP(); clientIP != "" {
		req.Header.Set("X-Forwarded-For", clientIP)
	}
	return req, nil
}

func codexTextPricingModels() map[string]struct{} {
	models := make(map[string]struct{})
	for _, pricing := range model.GetPricing() {
		// Existing pricing metadata distinguishes token-priced conversation
		// models from fixed-price media/task APIs without guessing by model name.
		if pricing.QuotaType == 0 {
			models[pricing.ModelName] = struct{}{}
		}
	}
	return models
}

func isCodexConversationModel(candidate dto.OpenAIModels, textPricing map[string]struct{}) bool {
	id := strings.TrimSpace(candidate.Id)
	if id == "" {
		return false
	}
	if _, ok := textPricing[id]; !ok {
		return false
	}
	if len(candidate.SupportedEndpointTypes) == 0 {
		return true
	}
	for _, endpoint := range candidate.SupportedEndpointTypes {
		switch endpoint {
		case "openai", "openai-response", "anthropic", "gemini":
			return true
		}
	}
	return false
}

func buildCodexModel(candidate dto.OpenAIModels, index int) map[string]any {
	displayName := strings.TrimSpace(candidate.Id)
	entry := map[string]any{
		"slug":                              candidate.Id,
		"display_name":                      displayName,
		"description":                       "Available through MadAPI: " + displayName,
		"prefer_websockets":                 true,
		"support_verbosity":                 true,
		"default_verbosity":                 "medium",
		"web_search_tool_type":              "text_and_image",
		"input_modalities":                  []string{"text"},
		"supports_image_detail_original":    false,
		"truncation_policy":                 map[string]any{"mode": "tokens", "limit": 10000},
		"supports_parallel_tool_calls":      true,
		"tool_mode":                         nil,
		"multi_agent_version":               nil,
		"use_responses_lite":                false,
		"include_skills_usage_instructions": true,
		"auto_review_model_override":        nil,
		"context_window":                    1000000,
		"max_context_window":                1000000,
		"auto_compact_token_limit":          nil,
		"comp_hash":                         "madapi-codex-compat-v1",
		"reasoning_summary_format":          "none",
		"default_reasoning_summary":         "none",
		"default_reasoning_level":           "high",
		"supported_reasoning_levels": []any{
			map[string]any{"effort": "low", "description": codexReasoningDescription("low")},
			map[string]any{"effort": "medium", "description": codexReasoningDescription("medium")},
			map[string]any{"effort": "high", "description": codexReasoningDescription("high")},
			map[string]any{"effort": "xhigh", "description": codexReasoningDescription("xhigh")},
			map[string]any{"effort": "max", "description": codexReasoningDescription("max")},
		},
		"shell_type":                           "shell_command",
		"visibility":                           "list",
		"minimal_client_version":               "0.0.0",
		"supported_in_api":                     true,
		"priority":                             100 + index,
		"model_messages":                       map[string]any{"instructions_template": codexCompatibilityBaseInstructions},
		"experimental_supported_tools":         []any{},
		"supports_search_tool":                 false,
		"default_service_tier":                 nil,
		"service_tiers":                        []any{},
		"additional_speed_tiers":               []any{},
		"supports_reasoning_summary_parameter": false,
		"supports_reasoning_summaries":         false,
		"base_instructions":                    codexCompatibilityBaseInstructions,
	}
	applyCodexCapabilities(entry, candidate)
	return entry
}

const codexCompatibilityBaseInstructions = "You are a coding agent. Follow the user's instructions, use the provided tools when needed, preserve existing work, make focused changes, and report verification accurately."

func proxyCodexErrorResponse(c *gin.Context, resp *http.Response) {
	for key, values := range resp.Header {
		if strings.EqualFold(key, "Content-Length") || strings.EqualFold(key, "Transfer-Encoding") || strings.EqualFold(key, "Connection") {
			continue
		}
		for _, value := range values {
			c.Writer.Header().Add(key, value)
		}
	}
	c.Status(resp.StatusCode)
	_, _ = io.Copy(c.Writer, io.LimitReader(resp.Body, 8<<20))
}

func codexCompatibilityError(c *gin.Context, status int, err error) {
	if status < http.StatusBadRequest {
		status = http.StatusInternalServerError
	}
	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    "codex_compatibility_error",
			"message": err.Error(),
		},
	})
}
