package controller

import (
	"net/http"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

const codexCockpitHeader = "X-MadAPI-Codex-Cockpit"

// ListCodexModels exposes the established /codex catalog contract without a
// CPA runtime. OAuth follows the token's live text inventory; API login keeps
// the stable eight-shell projection used by existing installers.
func ListCodexModels(c *gin.Context) {
	available, err := codexAvailableModelIDs(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	models := make([]map[string]any, 0, len(available))
	if strings.TrimSpace(c.GetHeader(codexCockpitHeader)) == "1" {
		availableSet := make(map[string]struct{}, len(available))
		for _, id := range available {
			availableSet[strings.ToLower(id)] = struct{}{}
		}
		for _, shell := range codexAPIShellOrder() {
			upstream := constant.CodexAPIModelSlots[shell]
			if _, ok := availableSet[strings.ToLower(upstream)]; !ok {
				continue
			}
			models = append(models, buildNativeCodexModel(shell, upstream, len(models)+1))
		}
	} else {
		for _, id := range available {
			if isCodexConversationModelID(id) {
				models = append(models, buildNativeCodexModel(id, id, len(models)+1))
			}
		}

	}

	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"models": models})
}

func codexAvailableModelIDs(c *gin.Context) ([]string, error) {
	acceptUnsetRatioModel := operation_setting.SelfUseModeEnabled
	if !acceptUnsetRatioModel {
		if userID := c.GetInt("id"); userID > 0 {
			userSettings, _ := model.GetUserSetting(userID, false)
			acceptUnsetRatioModel = userSettings.AcceptUnsetRatioModel
		}
	}
	groups, err := getModelListGroups(c)
	if err != nil {
		return nil, err
	}
	modelLimitEnabled := common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled)
	var tokenModelLimit map[string]bool
	if modelLimitEnabled {
		value, _ := common.GetContextKey(c, constant.ContextKeyTokenModelLimit)
		tokenModelLimit, _ = value.(map[string]bool)
		if tokenModelLimit == nil {
			tokenModelLimit = map[string]bool{}
		}
	}

	result := make([]string, 0)
	for _, modelName := range service.GetGroupsEnabledModels(groups.ownerGroups) {
		if modelLimitEnabled {
			matchingName := ratio_setting.FormatMatchingModelName(modelName)
			if !tokenModelLimit[modelName] && !tokenModelLimit[matchingName] {
				continue
			}
		}
		if !acceptUnsetRatioModel && !helper.HasModelBillingConfig(modelName) {
			continue
		}
		result = append(result, modelName)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return strings.ToLower(result[i]) < strings.ToLower(result[j])
	})
	return result, nil
}

func codexAPIShellOrder() []string {
	return []string{
		"gpt-5.5", "gpt-5.4", "gpt-5.6-sol", "gpt-5.6-terra",
		"gpt-5.6-luna", "gpt-5.4-mini", "gpt-5.3-codex", "gpt-5.2",
	}
}

func isCodexConversationModelID(id string) bool {
	lower := strings.ToLower(strings.TrimSpace(id))
	if lower == "" {
		return false
	}
	for _, marker := range []string{"image", "video", "seedance", "sora", "veo", "kling", "hailuo"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

func buildNativeCodexModel(slug, displayName string, priority int) map[string]any {
	return map[string]any{
		"slug":                    slug,
		"display_name":            displayName,
		"description":             "Available through MadAPI: " + displayName,
		"default_reasoning_level": "medium",
		"supported_reasoning_levels": []map[string]string{
			{"effort": "low", "description": "Low reasoning"},
			{"effort": "medium", "description": "Medium reasoning"},
			{"effort": "high", "description": "High reasoning"},
		},
		"shell_type":                   "shell_command",
		"visibility":                   "list",
		"supported_in_api":             true,
		"priority":                     priority,
		"supports_parallel_tool_calls": true,
		"input_modalities":             []string{"text", "image"},
		"supports_search_tool":         true,
		"prefer_websockets":            false,
	}
}
