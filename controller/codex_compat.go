package controller

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/codexclientmodels"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

const (
	defaultCodexInternalBaseURL   = "http://127.0.0.1:3000/v1"
	codexNativeCompactInstruction = "Create a faithful compact state summary for continuing this coding session. Preserve user requirements, decisions, file paths, code changes, tool results, unresolved work, and safety constraints. Do not invent facts. Return only the compact summary text."
)

var (
	codexInternalBaseURL     = defaultCodexInternalBaseURL
	codexTextPricingProvider = codexTextPricingModels
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
		func(id string) bool { return codexSupportsNativeSearch(id) },
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
	responsesReq, err := helper.GetAndValidateResponsesRequest(c)
	if err != nil {
		codexCompatibilityError(c, http.StatusBadRequest, err)
		return
	}
	if !isCodexConversationModel(dto.OpenAIModels{Id: responsesReq.Model}, codexTextPricingProvider()) {
		codexCompatibilityError(c, http.StatusBadRequest, fmt.Errorf("model %q is not a Codex conversation model", responsesReq.Model))
		return
	}

	body, err := relayconvert.NormalizeCodexResponsesRequest(originalBody)
	if err != nil {
		codexCompatibilityError(c, http.StatusBadRequest, err)
		return
	}
	innerReq, err := newCodexInternalRequest(c, http.MethodPost, "/responses", bytes.NewReader(body))
	if err != nil {
		codexCompatibilityError(c, http.StatusInternalServerError, err)
		return
	}
	innerReq.Header.Set("Content-Type", "application/json")

	resp, err := codexInternalHTTPClient.Do(innerReq)
	if err != nil {
		codexCompatibilityError(c, http.StatusBadGateway, fmt.Errorf("native Responses request failed: %w", err))
		return
	}
	isStream := lo.FromPtrOr(responsesReq.Stream, false)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		if isStream {
			writeCodexFailureStream(c, codexUpstreamErrorMessage(resp), "upstream_error")
			return
		}
		proxyCodexErrorResponse(c, resp)
		return
	}
	defer resp.Body.Close()
	copyCodexTraceHeaders(c, resp)
	if err = proxyCodexNativeResponses(c, resp, originalBody, isStream); err != nil && !c.Writer.Written() {
		codexCompatibilityError(c, http.StatusBadGateway, err)
	}
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
	body, err = relayconvert.NormalizeCodexResponsesRequest(body)
	if err != nil {
		codexCompatibilityError(c, http.StatusBadRequest, err)
		return
	}
	innerReq, err := newCodexInternalRequest(c, http.MethodPost, "/responses", bytes.NewReader(body))
	if err != nil {
		codexCompatibilityError(c, http.StatusInternalServerError, err)
		return
	}
	innerReq.Header.Set("Content-Type", "application/json")
	resp, err := codexInternalHTTPClient.Do(innerReq)
	if err != nil {
		codexCompatibilityError(c, http.StatusBadGateway, fmt.Errorf("native Responses compact request failed: %w", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		proxyCodexErrorResponse(c, resp)
		return
	}
	copyCodexTraceHeaders(c, resp)
	if err = writeCodexNativeCompactResponse(c, resp); err != nil && !c.Writer.Written() {
		codexCompatibilityError(c, http.StatusBadGateway, err)
	}
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
		ParallelToolCalls:    compactReq.ParallelToolCalls,
		ServiceTier:          compactReq.ServiceTier,
		PromptCacheKey:       compactReq.PromptCacheKey,
		PromptCacheOptions:   compactReq.PromptCacheOptions,
		PromptCacheRetention: compactReq.PromptCacheRetention,
		Stream:               &stream,
	}
	return common.Marshal(request)
}

func writeCodexNativeCompactResponse(c *gin.Context, resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	var responses dto.OpenAIResponsesResponse
	if err = common.Unmarshal(body, &responses); err != nil {
		return fmt.Errorf("invalid native compact response: %w", err)
	}
	summary := strings.TrimSpace(service.ExtractOutputTextFromResponses(&responses))
	if summary == "" {
		return fmt.Errorf("native compact response omitted summary text")
	}
	output, err := common.Marshal([]map[string]any{{"type": "compaction_summary", "summary": summary}})
	if err != nil {
		return err
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
		return err
	}
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	_, err = c.Writer.Write(encoded)
	return err
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

func proxyCodexNativeResponses(c *gin.Context, resp *http.Response, originalRequest []byte, stream bool) error {
	copyCodexResponseHeaders(c, resp)
	if stream || strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return proxyCodexNativeResponsesStream(c, resp, originalRequest)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	restored, err := relayconvert.RestoreCodexResponsesPayload(originalRequest, body)
	if err != nil {
		return fmt.Errorf("invalid native Responses payload: %w", err)
	}
	c.Status(resp.StatusCode)
	_, err = c.Writer.Write(restored)
	return err
}

func proxyCodexNativeResponsesStream(c *gin.Context, resp *http.Response, originalRequest []byte) error {
	const maxCodexSSEFrameBytes = 32 << 20
	c.Status(resp.StatusCode)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 32<<20)
	frame := make([][]byte, 0, 4)
	responseID := ""
	started := false
	terminal := false
	clientToolSearchItems := make(map[string]bool)
	hostedWebSearchItems := make(map[string]bool)
	customToolItems := make(map[string]bool)
	frameBytes := 0

	flushFrame := func() error {
		if len(frame) == 0 {
			return nil
		}
		if terminal {
			frame = frame[:0]
			return nil
		}
		eventType := ""
		payload := []byte(nil)
		for i, rawLine := range frame {
			line := rawLine
			if bytes.HasPrefix(line, []byte("event:")) {
				eventType = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
			}
			if bytes.HasPrefix(line, []byte("data:")) {
				candidate := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
				if len(candidate) > 0 && !bytes.Equal(candidate, []byte("[DONE]")) {
					restored, restoreErr := relayconvert.RestoreCodexResponsesPayload(originalRequest, candidate)
					if restoreErr != nil {
						writeCodexTerminalEvent(c, "response.failed", responseID, "invalid upstream Responses event", "protocol_error")
						terminal = true
						frame = frame[:0]
						frameBytes = 0
						return nil
					}
					candidate = restored
					frame[i] = append([]byte("data: "), restored...)
					payload = candidate
				}
			}
		}

		suppressFrame := false
		if len(payload) > 0 && json.Valid(payload) {
			var event map[string]any
			if json.Unmarshal(payload, &event) == nil {
				if value, ok := event["type"].(string); ok && value != "" {
					eventType = value
				}
				if response, ok := event["response"].(map[string]any); ok {
					if id, ok := response["id"].(string); ok && id != "" {
						responseID = id
					}
					if outputValue, exists := response["output"]; exists {
						if _, ok := outputValue.([]any); !ok {
							writeCodexTerminalEvent(c, "response.failed", responseID, "completed response contained an invalid output snapshot", "protocol_error")
							terminal = true
							frame = frame[:0]
							frameBytes = 0
							return nil
						}
					}
				}
				if id, ok := event["id"].(string); ok && id != "" && responseID == "" {
					responseID = id
				}
				if item, ok := event["item"].(map[string]any); ok {
					itemID, _ := item["id"].(string)
					switch item["type"] {
					case "tool_search_call":
						clientToolSearchItems[itemID] = true
						if strings.HasPrefix(itemID, "tsc_") {
							clientToolSearchItems["fc_"+strings.TrimPrefix(itemID, "tsc_")] = true
						}
					case "web_search_call":
						hostedWebSearchItems[itemID] = true
						if strings.HasPrefix(itemID, "ws_") {
							hostedWebSearchItems["fc_"+strings.TrimPrefix(itemID, "ws_")] = true
						}
					case "custom_tool_call":
						customToolItems[itemID] = true
						if strings.HasPrefix(itemID, "ctc_") {
							customToolItems["fc_"+strings.TrimPrefix(itemID, "ctc_")] = true
						}
					}
				}
				if itemID, ok := event["item_id"].(string); ok {
					if (clientToolSearchItems[itemID] || hostedWebSearchItems[itemID]) &&
						(eventType == "response.function_call_arguments.delta" || eventType == "response.function_call_arguments.done") {
						suppressFrame = true
					}
					if customToolItems[itemID] && eventType == "response.function_call_arguments.delta" {
						suppressFrame = true
					}
					if customToolItems[itemID] && eventType == "response.function_call_arguments.done" {
						eventType = "response.custom_tool_call_input.done"
						event["type"] = eventType
						event["input"] = codexCustomInputFromArguments(event["arguments"])
						delete(event, "arguments")
						if encoded, encodeErr := json.Marshal(event); encodeErr == nil {
							payload = encoded
							for lineIndex, rawLine := range frame {
								if bytes.HasPrefix(rawLine, []byte("event:")) {
									frame[lineIndex] = []byte("event: " + eventType)
								}
								if bytes.HasPrefix(rawLine, []byte("data:")) {
									frame[lineIndex] = append([]byte("data: "), encoded...)
								}
							}
						}
					}
				}
			}
		}
		if suppressFrame {
			frame = frame[:0]
			frameBytes = 0
			return nil
		}

		if eventType == "error" || eventType == "response.error" {
			message := codexEventErrorMessage(payload)
			writeCodexTerminalEvent(c, "response.failed", responseID, message, "upstream_error")
			terminal = true
			frame = frame[:0]
			return nil
		}
		if strings.HasPrefix(eventType, "response.") {
			started = true
		}
		if eventType == "response.completed" || eventType == "response.failed" || eventType == "response.incomplete" {
			terminal = true
		}
		for _, line := range frame {
			if _, err := c.Writer.Write(append(line, '\n')); err != nil {
				return err
			}
		}
		if _, err := c.Writer.Write([]byte("\n")); err != nil {
			return err
		}
		c.Writer.Flush()
		frame = frame[:0]
		frameBytes = 0
		return nil
	}

	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(bytes.TrimSpace(line)) == 0 {
			if err := flushFrame(); err != nil {
				return err
			}
			continue
		}
		frameBytes += len(line) + 1
		if frameBytes > maxCodexSSEFrameBytes {
			writeCodexTerminalEvent(c, "response.failed", responseID, "upstream Responses event exceeded the translation limit", "translation_buffer_limit")
			return nil
		}
		frame = append(frame, line)
	}
	if err := scanner.Err(); err != nil && !terminal {
		writeCodexTerminalEvent(c, "response.incomplete", responseID, err.Error(), "stream_error")
		terminal = true
	}
	if err := flushFrame(); err != nil {
		return err
	}
	if !terminal {
		eventType := "response.failed"
		code := "empty_stream"
		message := "upstream stream ended before producing a complete response"
		if started {
			eventType = "response.incomplete"
			code = "stream_ended"
			message = "upstream stream ended before its terminal event"
		}
		writeCodexTerminalEvent(c, eventType, responseID, message, code)
	}
	return nil
}

func codexCustomInputFromArguments(value any) string {
	arguments, ok := value.(string)
	if !ok {
		return ""
	}
	var wrapper map[string]any
	if json.Unmarshal([]byte(arguments), &wrapper) == nil {
		if input, exists := wrapper["input"]; exists {
			if text, textOK := input.(string); textOK {
				return text
			}
			if encoded, err := json.Marshal(input); err == nil {
				return string(encoded)
			}
		}
	}
	return arguments
}

func codexUpstreamErrorMessage(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return "upstream request failed"
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		return fmt.Sprintf("upstream request failed with status %d", resp.StatusCode)
	}
	if message := codexEventErrorMessage(body); message != "" {
		return message
	}
	return strings.TrimSpace(string(body))
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

func writeCodexFailureStream(c *gin.Context, message string, code string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Status(http.StatusOK)
	writeCodexTerminalEvent(c, "response.failed", "", message, code)
}

func writeCodexTerminalEvent(c *gin.Context, eventType string, responseID string, message string, code string) {
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
	_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", eventType, encoded)
	c.Writer.Flush()
}

func copyCodexResponseHeaders(c *gin.Context, resp *http.Response) {
	for key, values := range resp.Header {
		if strings.EqualFold(key, "Content-Length") || strings.EqualFold(key, "Transfer-Encoding") || strings.EqualFold(key, "Connection") {
			continue
		}
		for _, value := range values {
			c.Writer.Header().Add(key, value)
		}
	}
}

func copyCodexTraceHeaders(c *gin.Context, resp *http.Response) {
	for _, header := range []string{"X-Oneapi-Request-Id", "X-Request-Id"} {
		if value := resp.Header.Get(header); value != "" {
			c.Header(header, value)
		}
	}
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
	req.Header.Set("X-MadAPI-Codex-Compat", relayconvert.CodexResponsesInternalMarker())
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
