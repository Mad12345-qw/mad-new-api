package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
)

const codexCockpitPathPrefix = "/codex/cockpit/v1/"

type codexCockpitModelSlot struct {
	ClientModel   string
	UpstreamModel string
	Candidate     dto.OpenAIModels
}

// API-key Codex Desktop sessions only retain a bounded set of official model
// shells after a cold restart. Keep the user-visible selection explicit and
// deterministic; OAuth sessions use the native /codex/v1 catalog instead.
var codexCockpitModelTargets = []struct {
	UpstreamModel string
	ClientModel   string
}{
	{UpstreamModel: "claude-fable-5", ClientModel: "gpt-5.5"},
	{UpstreamModel: "claude-opus-5", ClientModel: "gpt-5.4"},
	{UpstreamModel: "gpt-5.6-sol", ClientModel: "gpt-5.6-sol"},
	{UpstreamModel: "gpt-5.6-terra", ClientModel: "gpt-5.6-terra"},
	{UpstreamModel: "gpt-5.6-luna", ClientModel: "gpt-5.6-luna"},
	{UpstreamModel: "grok-4.5", ClientModel: "gpt-5.4-mini"},
	{UpstreamModel: "gpt-5.6-sol-pro", ClientModel: "gpt-5.3-codex"},
	{UpstreamModel: "gpt-5.6-terra-pro", ClientModel: "gpt-5.2"},
}

func isCodexCockpitModelShell(model string) bool {
	model = strings.TrimSpace(model)
	for _, target := range codexCockpitModelTargets {
		if strings.EqualFold(model, target.ClientModel) {
			return true
		}
	}
	return false
}

func allocateCodexCockpitModelSlots(models []dto.OpenAIModels) []codexCockpitModelSlot {
	available := make(map[string]dto.OpenAIModels, len(models))
	for _, candidate := range models {
		modelID := strings.TrimSpace(candidate.Id)
		if modelID == "" {
			continue
		}
		available[strings.ToLower(modelID)] = candidate
	}
	slots := make([]codexCockpitModelSlot, 0, len(codexCockpitModelTargets))
	for _, target := range codexCockpitModelTargets {
		candidate, ok := available[strings.ToLower(target.UpstreamModel)]
		if !ok {
			continue
		}
		slots = append(slots, codexCockpitModelSlot{
			ClientModel:   target.ClientModel,
			UpstreamModel: target.UpstreamModel,
			Candidate:     candidate,
		})
	}
	return slots
}

func loadCodexCockpitModelSlots(c *gin.Context) ([]codexCockpitModelSlot, error) {
	req, err := newCodexInternalRequest(c, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := codexInternalHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("model catalog request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model catalog request returned HTTP %d", resp.StatusCode)
	}

	var upstream codexOpenAIModelsResponse
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&upstream); err != nil {
		return nil, fmt.Errorf("invalid model catalog response: %w", err)
	}
	textPricing := codexTextPricingProvider()
	models := make([]dto.OpenAIModels, 0, len(upstream.Data))
	for _, candidate := range upstream.Data {
		if isCodexConversationModel(candidate, textPricing) {
			models = append(models, candidate)
		}
	}
	return allocateCodexCockpitModelSlots(models), nil
}

func CodexCockpitListModels(c *gin.Context) {
	slots, err := loadCodexCockpitModelSlots(c)
	if err != nil {
		codexCompatibilityError(c, http.StatusBadGateway, err)
		return
	}

	models := make([]map[string]any, 0, len(slots))
	for index, slot := range slots {
		entry := buildCodexModel(slot.Candidate, index)
		entry["slug"] = slot.ClientModel
		entry["display_name"] = slot.UpstreamModel
		entry["description"] = "Available through MadAPI: " + slot.UpstreamModel
		entry["prefer_websockets"] = false
		entry["priority"] = index + 1
		models = append(models, entry)
	}

	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, codexModelsResponse{Models: models})
}

func rewriteCodexCockpitRequestBody(c *gin.Context, raw []byte) ([]byte, error) {
	if c.Request == nil || c.Request.URL == nil || !strings.HasPrefix(c.Request.URL.Path, codexCockpitPathPrefix) {
		return raw, nil
	}

	var payload map[string]json.RawMessage
	if err := common.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	var requestedModel string
	if err := common.Unmarshal(payload["model"], &requestedModel); err != nil {
		return nil, fmt.Errorf("model is required")
	}
	if !isCodexCockpitModelShell(requestedModel) {
		return raw, nil
	}
	slots, err := loadCodexCockpitModelSlots(c)
	if err != nil {
		return nil, err
	}
	upstreamModel := requestedModel
	for _, slot := range slots {
		if strings.EqualFold(slot.ClientModel, requestedModel) {
			upstreamModel = slot.UpstreamModel
			break
		}
	}
	if strings.EqualFold(upstreamModel, requestedModel) {
		return raw, nil
	}
	modelJSON, err := common.Marshal(upstreamModel)
	if err != nil {
		return nil, err
	}
	payload["model"] = modelJSON
	rewritten, err := common.Marshal(payload)
	if err != nil {
		return nil, err
	}

	common.CleanupBodyStorage(c)
	c.Set(common.KeyRequestBody, rewritten)
	c.Request.Body = io.NopCloser(bytes.NewReader(rewritten))
	c.Request.ContentLength = int64(len(rewritten))
	return rewritten, nil
}
