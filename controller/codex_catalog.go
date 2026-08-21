package controller

import (
	_ "embed"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

// This snapshot is copied from router-for-me/CLIProxyAPI commit
// d9460a8df6c15175342ede3dc5c423eb2df11f58 (MIT). Keeping it build-pinned
// prevents a remote catalog update from silently changing advertised abilities.
//
//go:embed codex_models.json
var codexNativeModelCatalogJSON []byte

type codexNativeThinking struct {
	Levels []string `json:"levels"`
}

type codexNativeModelInfo struct {
	ID                       string               `json:"id"`
	OwnedBy                  string               `json:"owned_by"`
	Type                     string               `json:"type"`
	DisplayName              string               `json:"display_name"`
	Description              string               `json:"description"`
	InputTokenLimit          int                  `json:"inputTokenLimit"`
	OutputTokenLimit         int                  `json:"outputTokenLimit"`
	ContextLength            int                  `json:"context_length"`
	MaxCompletionTokens      int                  `json:"max_completion_tokens"`
	SupportedInputModalities []string             `json:"supportedInputModalities"`
	Thinking                 *codexNativeThinking `json:"thinking"`
}

func (info codexNativeModelInfo) effectiveContextLength() int {
	if info.ContextLength > 0 {
		return info.ContextLength
	}
	return info.InputTokenLimit
}

func (info codexNativeModelInfo) effectiveMaxCompletionTokens() int {
	if info.MaxCompletionTokens > 0 {
		return info.MaxCompletionTokens
	}
	return info.OutputTokenLimit
}

var (
	codexNativeCatalogOnce sync.Once
	codexNativeCatalog     map[string][]codexNativeModelInfo
)

var codexValidatedImageInputModels = map[string]bool{
	"claude-fable-5":    true,
	"claude-opus-4-8":   true,
	"claude-opus-5":     true,
	"gemini-3.6-flash":  true,
	"gpt-5.5":           true,
	"gpt-5.6-luna":      true,
	"gpt-5.6-sol":       true,
	"gpt-5.6-sol-pro":   true,
	"gpt-5.6-terra":     true,
	"gpt-5.6-terra-pro": true,
	"grok-4.5":          true,
	"grok-4.6":          true,
	"kimi-k3":           true,
}

var codexNativeSearchModels = map[string]bool{
	"claude-haiku-4-5":    true,
	"claude-fable-5":      true,
	"claude-opus-4-8":     true,
	"claude-opus-5":       true,
	"claude-sonnet-5":     true,
	"deepseek-v4-flash":   true,
	"deepseek-v4-pro":     true,
	"gemini-3.6-flash":    true,
	"glm-5-2":             true,
	"glm-5.2":             true,
	"glm-5.3":             true,
	"gpt-5.5":             true,
	"gpt-5.6-luna":        true,
	"gpt-5.6-sol":         true,
	"gpt-5.6-sol-pro":     true,
	"gpt-5.6-terra":       true,
	"gpt-5.6-terra-pro":   true,
	"grok-4.5":            true,
	"grok-4.6":            true,
	"kimi-k3":             true,
	"qwen3.8-max-preview": true,
}

var codexNativeVerbosityModels = map[string]bool{
	"gpt-5.5":           true,
	"gpt-5.6-luna":      true,
	"gpt-5.6-sol":       true,
	"gpt-5.6-sol-pro":   true,
	"gpt-5.6-terra":     true,
	"gpt-5.6-terra-pro": true,
}

// These aliases are absent from the pinned Codex capability snapshot but are documented
// by their vendors. The MadAPI channel mapping is responsible for translating
// glm-5-2 to the official glm-5.2 identifier.
// DeepSeek: https://api-docs.deepseek.com/quick_start/pricing
// GLM: https://docs.bigmodel.cn/cn/guide/models/text/glm-5.2
// Qwen: https://help.aliyun.com/zh/model-studio/text-generation-model/
var codexOfficialCapabilityOverrides = map[string]codexNativeModelInfo{
	"deepseek-v4-flash": {
		ID:                  "deepseek-v4-flash",
		DisplayName:         "DeepSeek V4 Flash",
		Description:         "DeepSeek V4 Flash with tool calling and optional thinking mode",
		ContextLength:       1048576,
		MaxCompletionTokens: 393216,
		Thinking:            &codexNativeThinking{Levels: []string{"none", "high"}},
	},
	"deepseek-v4-pro": {
		ID:                  "deepseek-v4-pro",
		DisplayName:         "DeepSeek V4 Pro",
		Description:         "DeepSeek V4 Pro with tool calling and optional thinking mode",
		ContextLength:       1048576,
		MaxCompletionTokens: 393216,
		Thinking:            &codexNativeThinking{Levels: []string{"none", "high"}},
	},
	"glm-5-2": {
		ID:                  "glm-5-2",
		DisplayName:         "GLM-5.2",
		Description:         "GLM-5.2 long-context coding and agent model",
		ContextLength:       1048576,
		MaxCompletionTokens: 131072,
		Thinking:            &codexNativeThinking{Levels: []string{"none", "high"}},
	},
	"qwen3.8-max-preview": {
		ID:          "qwen3.8-max-preview",
		DisplayName: "Qwen3.8 Max Preview",
		Description: "Qwen3.8 Max Preview for advanced reasoning and agent development",
		Thinking:    &codexNativeThinking{Levels: []string{"high"}},
	},
}

type codexCapabilityAlias struct {
	BaseID      string
	DisplayName string
}

var codexCapabilityAliases = map[string]codexCapabilityAlias{
	"gpt-5.6-sol-pro":   {BaseID: "gpt-5.6-sol", DisplayName: "GPT 5.6 Sol Pro"},
	"gpt-5.6-terra-pro": {BaseID: "gpt-5.6-terra", DisplayName: "GPT 5.6 Terra Pro"},
}

func codexModelInfoForRequestedID(id string, info codexNativeModelInfo) *codexNativeModelInfo {
	info.ID = id
	if alias, ok := codexCapabilityAliases[id]; ok {
		info.DisplayName = alias.DisplayName
	}
	return &info
}

func loadCodexNativeCatalog() map[string][]codexNativeModelInfo {
	codexNativeCatalogOnce.Do(func() {
		codexNativeCatalog = make(map[string][]codexNativeModelInfo)
		var groups map[string][]codexNativeModelInfo
		if common.Unmarshal(codexNativeModelCatalogJSON, &groups) != nil {
			return
		}
		for _, models := range groups {
			for _, info := range models {
				id := strings.TrimSpace(info.ID)
				if id == "" {
					continue
				}
				codexNativeCatalog[id] = append(codexNativeCatalog[id], info)
			}
		}
	})
	return codexNativeCatalog
}

func codexCatalogModel(candidate dto.OpenAIModels) *codexNativeModelInfo {
	id := strings.TrimSpace(candidate.Id)
	lookupID := id
	if alias, ok := codexCapabilityAliases[id]; ok {
		lookupID = alias.BaseID
	}
	if override, ok := codexOfficialCapabilityOverrides[lookupID]; ok {
		return codexModelInfoForRequestedID(id, override)
	}
	matches := loadCodexNativeCatalog()[lookupID]
	if len(matches) == 0 {
		return nil
	}
	owner := strings.ToLower(strings.TrimSpace(candidate.OwnedBy))
	for index := range matches {
		infoOwner := strings.ToLower(strings.TrimSpace(matches[index].OwnedBy))
		infoType := strings.ToLower(strings.TrimSpace(matches[index].Type))
		if owner != "" && (owner == infoOwner || owner == infoType || strings.Contains(owner, infoType)) {
			return codexModelInfoForRequestedID(id, matches[index])
		}
	}
	best := 0
	for index := 1; index < len(matches); index++ {
		bestScore := matches[best].effectiveContextLength()
		candidateScore := matches[index].effectiveContextLength()
		if matches[best].Thinking != nil {
			bestScore += len(matches[best].Thinking.Levels) * 1000
		}
		if matches[index].Thinking != nil {
			candidateScore += len(matches[index].Thinking.Levels) * 1000
		}
		if candidateScore > bestScore {
			best = index
		}
	}
	return codexModelInfoForRequestedID(id, matches[best])
}

func codexReasoningDescription(level string) string {
	switch level {
	case "none":
		return "No reasoning"
	case "minimal":
		return "Fastest responses with minimal reasoning"
	case "low":
		return "Fast responses with lighter reasoning"
	case "medium":
		return "Balances speed and reasoning depth for everyday tasks"
	case "high":
		return "Greater reasoning depth for complex problems"
	case "xhigh":
		return "Extra high reasoning depth for complex problems"
	case "max", "ultra":
		return "Maximum available reasoning depth for complex problems"
	default:
		return level
	}
}

func codexReasoningLevels(info *codexNativeModelInfo) ([]any, string) {
	if info == nil || info.Thinking == nil {
		return nil, ""
	}
	allowed := map[string]bool{
		"none": true, "minimal": true, "low": true, "medium": true,
		"high": true, "xhigh": true, "max": true, "ultra": true,
	}
	levels := make([]any, 0, len(info.Thinking.Levels))
	defaultLevel := ""
	for _, raw := range info.Thinking.Levels {
		level := strings.ToLower(strings.TrimSpace(raw))
		if !allowed[level] {
			continue
		}
		levels = append(levels, map[string]any{
			"effort":      level,
			"description": codexReasoningDescription(level),
		})
		if defaultLevel == "" && level != "none" {
			defaultLevel = level
		}
		if level == "high" {
			defaultLevel = level
		}
	}
	if len(levels) == 0 {
		return nil, ""
	}
	if defaultLevel == "" {
		defaultLevel = levels[0].(map[string]any)["effort"].(string)
	}
	return levels, defaultLevel
}

func applyCodexCapabilities(entry map[string]any, candidate dto.OpenAIModels) {
	entry["comp_hash"] = "madapi-codex-native-v1"
	info := codexCatalogModel(candidate)
	entry["supports_search_tool"] = codexNativeSearchModels[strings.ToLower(strings.TrimSpace(candidate.Id))]
	if info == nil {
		return
	}
	entry["support_verbosity"] = codexNativeVerbosityModels[strings.ToLower(strings.TrimSpace(candidate.Id))]
	if value := strings.TrimSpace(info.DisplayName); value != "" {
		entry["display_name"] = value
	}
	if value := strings.TrimSpace(info.Description); value != "" {
		entry["description"] = value
	}
	if contextLength := info.effectiveContextLength(); contextLength > 0 {
		entry["context_window"] = contextLength
		entry["max_context_window"] = contextLength
	}
	if maxCompletionTokens := info.effectiveMaxCompletionTokens(); maxCompletionTokens > 0 {
		entry["max_completion_tokens"] = maxCompletionTokens
	}
	modalities := make([]string, 0, 2)
	supportsImage := false
	seen := make(map[string]bool)
	for _, raw := range info.SupportedInputModalities {
		modality := strings.ToLower(strings.TrimSpace(raw))
		if (modality != "text" && modality != "image") || seen[modality] {
			continue
		}
		seen[modality] = true
		modalities = append(modalities, modality)
		supportsImage = supportsImage || modality == "image"
	}
	if len(modalities) > 0 {
		entry["input_modalities"] = modalities
		entry["supports_image_detail_original"] = supportsImage
	} else if codexValidatedImageInputModels[strings.TrimSpace(candidate.Id)] {
		entry["input_modalities"] = []string{"text", "image"}
		entry["supports_image_detail_original"] = true
	}
	if levels, defaultLevel := codexReasoningLevels(info); len(levels) > 0 {
		entry["supported_reasoning_levels"] = levels
		entry["default_reasoning_level"] = defaultLevel
	}
}
