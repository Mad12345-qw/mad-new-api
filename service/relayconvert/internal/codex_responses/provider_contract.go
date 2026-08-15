package codexresponses

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

type ProviderContract string

const (
	ProviderContractOpenAI       ProviderContract = "openai"
	ProviderContractCodex        ProviderContract = "codex"
	ProviderContractXAI          ProviderContract = "xai"
	ProviderContractClaude       ProviderContract = "claude"
	ProviderContractGemini       ProviderContract = "gemini"
	ProviderContractDeepSeek     ProviderContract = "deepseek"
	ProviderContractMoonshot     ProviderContract = "moonshot"
	ProviderContractOpenAICompat ProviderContract = "openai_compat"
)

const codexInputItemIDLimit = 64

const (
	codexInputItemIDOccupied uint8 = 1 << iota
	codexInputItemIDPreserved
)

// NormalizeOpenAIResponsesRequestForContract applies only the compatibility
// rules required by the selected upstream protocol. It never selects a model,
// channel, account, billing path, or retry policy.
func NormalizeOpenAIResponsesRequestForContract(rawJSON []byte, contract ProviderContract) ([]byte, error) {
	switch contract {
	case ProviderContractCodex:
		return normalizeNativeOpenAIResponsesRequest(rawJSON)
	case ProviderContractOpenAI:
		return NormalizeOpenAIResponsesRequestForProvider(rawJSON)
	case ProviderContractXAI:
		return normalizeXAIResponsesRequest(rawJSON)
	case ProviderContractClaude, ProviderContractGemini, ProviderContractDeepSeek,
		ProviderContractMoonshot, ProviderContractOpenAICompat:
		return NormalizeOpenAIResponsesRequestForProvider(rawJSON)
	default:
		return NormalizeOpenAIResponsesRequestForProvider(rawJSON)
	}
}

func normalizeNativeOpenAIResponsesRequest(rawJSON []byte) ([]byte, error) {
	var root map[string]any
	if err := common.Unmarshal(rawJSON, &root); err != nil {
		return nil, err
	}
	delete(root, "previous_response_id")
	normalizeCodexHostedToolAliases(root["tools"])
	normalizeCodexHostedToolAliases(root["tool_choice"])
	sanitizeCodexInputItemIDs(root)
	return common.Marshal(root)
}

func normalizeCodexHostedToolAliases(value any) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			normalizeCodexHostedToolAliases(item)
		}
	case map[string]any:
		switch strings.TrimSpace(stringValue(typed["type"])) {
		case "web_search_preview", "web_search_preview_2025_03_11":
			typed["type"] = "web_search"
		}
		if tools, ok := typed["tools"]; ok {
			normalizeCodexHostedToolAliases(tools)
		}
	}
}

func sanitizeCodexInputItemIDs(root map[string]any) {
	items, ok := root["input"].([]any)
	if !ok {
		return
	}

	idStates := make(map[string]uint8, len(items))
	for _, value := range items {
		item, ok := value.(map[string]any)
		if !ok || shouldDropCodexEncryptedReasoningItem(item) {
			continue
		}
		originalID, ok := item["id"].(string)
		if !ok {
			continue
		}
		id := normalizeCodexInputItemID(item, originalID)
		state := idStates[id]
		if id == originalID {
			state |= codexInputItemIDPreserved
		}
		if len([]rune(id)) <= codexInputItemIDLimit {
			state |= codexInputItemIDOccupied
		}
		if state != 0 {
			idStates[id] = state
		}
	}

	mapped := make(map[string]string)
	collisionMapped := make(map[string]string)
	rebuilt := make([]any, 0, len(items))
	for _, value := range items {
		item, ok := value.(map[string]any)
		if !ok {
			rebuilt = append(rebuilt, value)
			continue
		}
		if shouldDropCodexEncryptedReasoningItem(item) {
			continue
		}

		originalID, ok := item["id"].(string)
		if !ok {
			rebuilt = append(rebuilt, item)
			continue
		}
		id := normalizeCodexInputItemID(item, originalID)
		if id != originalID && idStates[id]&codexInputItemIDPreserved != 0 {
			collisionID, exists := collisionMapped[id]
			if !exists {
				for attempt := 0; ; attempt++ {
					collisionID = codexInputItemIDWithHashSuffix(id, attempt)
					if idStates[collisionID]&codexInputItemIDOccupied != 0 {
						continue
					}
					collisionMapped[id] = collisionID
					idStates[collisionID] |= codexInputItemIDOccupied
					break
				}
			}
			id = collisionID
		}
		if len([]rune(id)) > codexInputItemIDLimit {
			shortened, exists := mapped[id]
			if !exists {
				for attempt := 0; ; attempt++ {
					shortened = codexInputItemIDWithHashSuffix(id, attempt)
					if idStates[shortened]&codexInputItemIDOccupied == 0 {
						break
					}
				}
				mapped[id] = shortened
				idStates[shortened] |= codexInputItemIDOccupied
			}
			id = shortened
		}
		if id != originalID {
			item["id"] = id
		}
		rebuilt = append(rebuilt, item)
	}
	root["input"] = rebuilt
}

func normalizeCodexInputItemID(item map[string]any, id string) string {
	var prefix string
	switch strings.TrimSpace(stringValue(item["type"])) {
	case "message":
		prefix = "msg"
	case "reasoning":
		prefix = "rs"
	case "function_call":
		prefix = "fc"
	case "web_search_call":
		prefix = "ws"
	case "custom_tool_call":
		prefix = "ctc"
	case "custom_tool_call_output":
		prefix = "ctco"
	default:
		return id
	}
	if id == "" || hasCodexInputItemIDPrefix(id, prefix) {
		return id
	}
	return prefix + "_" + id
}

func hasCodexInputItemIDPrefix(id, prefix string) bool {
	if id == prefix {
		return true
	}
	if len(id) <= len(prefix) || !strings.HasPrefix(id, prefix) {
		return false
	}
	return id[len(prefix)] == '_' || id[len(prefix)] == '-'
}

func shouldDropCodexEncryptedReasoningItem(item map[string]any) bool {
	if strings.TrimSpace(stringValue(item["type"])) != "reasoning" {
		return false
	}
	id, ok := item["id"].(string)
	if !ok || len([]rune(id)) <= codexInputItemIDLimit {
		return false
	}
	encrypted, ok := item["encrypted_content"].(string)
	return ok && encrypted != ""
}

func codexInputItemIDWithHashSuffix(id string, attempt int) string {
	hashInput := id
	if attempt > 0 {
		hashInput += "\x00" + strconv.Itoa(attempt)
	}
	sum := sha256.Sum256([]byte(hashInput))
	suffix := "_" + hex.EncodeToString(sum[:8])
	runes := []rune(id)
	prefixLength := codexInputItemIDLimit - len(suffix)
	if len(runes) < prefixLength {
		prefixLength = len(runes)
	}
	return string(runes[:prefixLength]) + suffix
}

func normalizeXAIResponsesRequest(rawJSON []byte) ([]byte, error) {
	var root map[string]any
	if err := common.Unmarshal(rawJSON, &root); err != nil {
		return nil, err
	}
	delete(root, "previous_response_id")

	tools := filterXAIResponsesTools(root["tools"])
	if len(tools) == 0 {
		delete(root, "tools")
	} else {
		root["tools"] = tools
	}
	if input, ok := root["input"].([]any); ok {
		filtered := make([]any, 0, len(input))
		for _, value := range input {
			item, ok := value.(map[string]any)
			if !ok {
				filtered = append(filtered, value)
				continue
			}
			switch strings.TrimSpace(stringValue(item["type"])) {
			case "tool_search_call":
				continue
			case "tool_search_output":
				loaded := filterXAIResponsesTools(item["tools"])
				if len(loaded) > 0 {
					filtered = append(filtered, map[string]any{"type": "additional_tools", "tools": loaded})
				}
				continue
			case "additional_tools":
				loaded := filterXAIResponsesTools(item["tools"])
				if len(loaded) == 0 {
					continue
				}
				item["tools"] = loaded
			case "custom_tool_call", "custom_tool_call_output":
				delete(item, "id")
			}
			filtered = append(filtered, item)
		}
		root["input"] = filtered
	}

	prepared, err := common.Marshal(root)
	if err != nil {
		return nil, err
	}
	prepared, err = normalizeOpenAIResponsesRequestForFunctionProvider(prepared, false)
	if err != nil {
		return nil, err
	}
	if err = common.Unmarshal(prepared, &root); err != nil {
		return nil, err
	}

	if choice, ok := root["tool_choice"].(map[string]any); ok {
		if normalized := normalizeXAIResponsesToolChoice(choice); normalized != nil {
			root["tool_choice"] = normalized
		} else {
			delete(root, "tool_choice")
		}
	}
	if len(anySlice(root["tools"])) == 0 {
		delete(root, "tools")
		delete(root, "tool_choice")
		delete(root, "parallel_tool_calls")
	}
	sanitizeCodexInputItemIDs(root)
	return common.Marshal(root)
}

func filterXAIResponsesTools(value any) []any {
	filtered := make([]any, 0)
	for _, value := range anySlice(value) {
		tool, ok := value.(map[string]any)
		if !ok {
			continue
		}
		copyTool := cloneMap(tool)
		toolType := strings.TrimSpace(stringValue(copyTool["type"]))
		switch toolType {
		case "namespace":
			children := filterXAIResponsesTools(copyTool["tools"])
			if len(children) == 0 {
				continue
			}
			copyTool["tools"] = children
		case "tool_search", "image_generation":
			continue
		case "custom":
			if strings.EqualFold(strings.TrimSpace(nativeToolName(copyTool)), "apply_patch") {
				continue
			}
		case "web_search_preview", "web_search_preview_2025_03_11":
			copyTool["type"] = "web_search"
			delete(copyTool, "external_web_access")
		case "web_search":
			delete(copyTool, "external_web_access")
		}
		filtered = append(filtered, copyTool)
	}
	return filtered
}

func normalizeXAIResponsesToolChoice(choice map[string]any) any {
	normalizeCodexHostedToolAliases(choice)
	normalizeNativeToolChoice(choice)
	choiceType := strings.TrimSpace(stringValue(choice["type"]))
	switch choiceType {
	case "tool_search", "image_generation":
		return nil
	case "custom":
		if strings.EqualFold(strings.TrimSpace(stringValue(choice["name"])), "apply_patch") {
			return nil
		}
		choice["type"] = "function"
		delete(choice, "format")
		return choice
	case "web_search":
		delete(choice, "external_web_access")
		return map[string]any{
			"type":  "allowed_tools",
			"mode":  "required",
			"tools": []any{choice},
		}
	case "allowed_tools":
		allowed := make([]any, 0)
		for _, value := range anySlice(choice["tools"]) {
			tool, ok := value.(map[string]any)
			if !ok {
				continue
			}
			tool = cloneMap(tool)
			normalizeCodexHostedToolAliases(tool)
			normalizeNativeToolChoice(tool)
			switch strings.TrimSpace(stringValue(tool["type"])) {
			case "tool_search", "image_generation":
				continue
			case "custom":
				if strings.EqualFold(strings.TrimSpace(stringValue(tool["name"])), "apply_patch") {
					continue
				}
				tool["type"] = "function"
				delete(tool, "format")
			case "web_search":
				delete(tool, "external_web_access")
			}
			allowed = append(allowed, tool)
		}
		if len(allowed) == 0 {
			return nil
		}
		choice["tools"] = allowed
		return choice
	default:
		return choice
	}
}
