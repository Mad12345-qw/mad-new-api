package codexresponses

import (
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// NormalizeOpenAIResponsesRequest performs only the lossless, provider-neutral
// work required at the /codex boundary. Provider-native tools, history item
// types, and identifiers must remain untouched until NewAPI selects a channel.
func NormalizeOpenAIResponsesRequest(rawJSON []byte) ([]byte, error) {
	var root map[string]any
	if err := common.Unmarshal(rawJSON, &root); err != nil {
		return nil, fmt.Errorf("invalid Responses request: %w", err)
	}
	// The compatibility layer is deliberately stateless. Codex carries the
	// complete current-turn context, so provider response identifiers are not
	// expanded, cached, or forwarded by this converter.
	delete(root, "previous_response_id")
	return common.Marshal(root)
}

// NormalizeOpenAIResponsesRequestForProvider flattens Codex-only tool
// declarations and history items after NewAPI has selected a non-native
// provider adapter. OpenAI and Codex API adapters must not call this function.
func NormalizeOpenAIResponsesRequestForProvider(rawJSON []byte) ([]byte, error) {
	return normalizeOpenAIResponsesRequestForFunctionProvider(rawJSON, false, false)
}

// NormalizeOpenAIResponsesRequestForChatProvider keeps the reasoning ownership
// fields needed when a Codex Responses history is replayed through a strict
// thinking-mode Chat Completions provider.
func NormalizeOpenAIResponsesRequestForChatProvider(rawJSON []byte) ([]byte, error) {
	return normalizeOpenAIResponsesRequestForFunctionProvider(rawJSON, false, true)
}

func normalizeOpenAIResponsesRequestForFunctionProvider(rawJSON []byte, preserveToolChoice bool, preserveReasoningOwnership bool) ([]byte, error) {
	var root map[string]any
	if err := common.Unmarshal(rawJSON, &root); err != nil {
		return nil, fmt.Errorf("invalid Responses request: %w", err)
	}
	// The compatibility layer is deliberately stateless. Codex must carry the
	// complete current-turn context; provider response identifiers are not
	// expanded, cached, or forwarded by this converter.
	delete(root, "previous_response_id")

	tools := make([]any, 0)
	appendTools := func(value any) {
		for _, tool := range anySlice(value) {
			for _, normalized := range normalizeNativeTool(tool, "") {
				tools = append(tools, normalized)
			}
		}
	}
	appendTools(root["tools"])

	if input, ok := root["input"].([]any); ok {
		normalizedInput := make([]any, 0, len(input))
		for _, value := range input {
			item, ok := value.(map[string]any)
			if !ok {
				normalizedInput = append(normalizedInput, value)
				continue
			}
			itemType := strings.TrimSpace(stringValue(item["type"]))
			sanitizeProviderHistoryItemFields(item, itemType, preserveReasoningOwnership)
			if itemType == "reasoning" {
				if _, exists := item["summary"]; !exists {
					item["summary"] = []any{}
				}
			}
			if itemType == "additional_tools" {
				appendTools(item["tools"])
				continue
			}
			if itemType == "tool_search_output" {
				loadedTools := make([]any, 0)
				for _, tool := range anySlice(item["tools"]) {
					loadedTools = append(loadedTools, normalizeNativeTool(tool, "")...)
				}
				tools = append(tools, loadedTools...)
				item["type"] = "function_call_output"
				item["output"] = nativeToolSearchOutputText(loadedTools, stringValue(item["status"]))
				// Function-call outputs are paired by call_id. A Codex tool-search
				// output id (tso_...) is not valid after changing the item type and
				// can also collide with the paired function-call id when rewritten.
				delete(item, "id")
				delete(item, "tools")
				delete(item, "status")
			}
			normalizeNativeHistoryItem(item)
			normalizedInput = append(normalizedInput, item)
		}
		root["input"] = normalizedInput
	}

	tools = dedupeNativeTools(tools)
	if choice, ok := root["tool_choice"].(map[string]any); ok {
		if preserveToolChoice {
			normalizeNativeToolChoice(choice)
			root["tool_choice"] = choice
		} else {
			tools, root["tool_choice"] = normalizeNativeToolChoicePolicy(tools, choice)
		}
	}
	if len(tools) > 0 {
		root["tools"] = tools
	} else {
		delete(root, "tools")
	}
	if root["tool_choice"] == nil {
		delete(root, "tool_choice")
	}
	return common.Marshal(root)
}

func sanitizeProviderHistoryItemFields(item map[string]any, itemType string, preserveReasoningOwnership bool) {
	var allowed map[string]struct{}
	switch itemType {
	case "message":
		allowed = fieldSet("type", "id", "role", "content")
	case "reasoning":
		allowed = fieldSet("type", "id", "summary", "encrypted_content")
	case "function_call":
		allowed = fieldSet("type", "id", "call_id", "name", "namespace", "arguments")
	case "function_call_output":
		allowed = fieldSet("type", "id", "call_id", "output")
	case "custom_tool_call":
		allowed = fieldSet("type", "id", "call_id", "name", "namespace", "input")
	case "custom_tool_call_output":
		allowed = fieldSet("type", "id", "call_id", "output")
	case "tool_search_call":
		allowed = fieldSet("type", "id", "call_id", "execution", "arguments")
	case "tool_search_output":
		allowed = fieldSet("type", "id", "call_id", "tools")
	case "web_search_call":
		allowed = fieldSet("type", "id", "action")
	case "additional_tools":
		allowed = fieldSet("type", "tools")
	default:
		delete(item, "status")
		return
	}
	if preserveReasoningOwnership {
		switch itemType {
		case "message":
			allowed["phase"] = struct{}{}
			allowed["reasoning_content"] = struct{}{}
		case "function_call", "custom_tool_call":
			allowed["reasoning_content"] = struct{}{}
		}
	}
	for key := range item {
		if _, ok := allowed[key]; !ok {
			delete(item, key)
		}
	}
}

func fieldSet(fields ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		result[field] = struct{}{}
	}
	return result
}

// RestoreOpenAIResponsesPayload restores Codex namespace and custom-tool
// semantics on a native Responses JSON object or SSE event payload.
func RestoreOpenAIResponsesPayload(originalRequest, rawJSON []byte) ([]byte, error) {
	var root map[string]any
	if err := common.Unmarshal(rawJSON, &root); err != nil {
		return nil, err
	}
	customTools := responsesCustomToolNames(originalRequest)
	restoreNativeResponseObject(root, originalRequest, customTools)
	return common.Marshal(root)
}

func normalizeNativeTool(value any, namespace string) []any {
	tool, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	toolType := strings.TrimSpace(stringValue(tool["type"]))
	if toolType != "namespace" {
		copyTool := cloneMap(tool)
		name := nativeToolName(copyTool)
		if namespace != "" && name != "" {
			setNativeToolName(copyTool, qualifyResponsesNamespaceToolName(namespace, name))
		}
		switch toolType {
		case "custom":
			copyTool["type"] = "function"
			copyTool["parameters"] = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input": map[string]any{"type": "string", "description": "Raw tool input."},
				},
				"required": []any{"input"},
			}
			delete(copyTool, "format")
		case "tool_search":
			copyTool["type"] = "function"
			copyTool["name"] = "tool_search"
			if strings.TrimSpace(stringValue(copyTool["description"])) == "" {
				copyTool["description"] = "Search for additional tools to load for the next turn."
			}
			if _, ok := copyTool["parameters"].(map[string]any); !ok {
				copyTool["parameters"] = map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string", "description": "Search query for tools to load."},
						"limit": map[string]any{"type": "number", "description": "Maximum number of tools to return."},
					},
					"required": []any{"query"},
				}
			}
		case "", "function":
			normalizeNativeFunctionParameters(copyTool)
		default:
			if namespace != "" {
				return nil
			}
			if nativeResponsesHostedToolType(toolType) {
				delete(copyTool, "namespace")
				delete(copyTool, "defer_loading")
				return []any{copyTool}
			}
			if name == "" {
				return nil
			}
			copyTool["type"] = "function"
			normalizeNativeFunctionParameters(copyTool)
		}
		delete(copyTool, "namespace")
		delete(copyTool, "defer_loading")
		return []any{copyTool}
	}

	namespaceName := strings.TrimSpace(stringValue(tool["name"]))
	if namespace != "" {
		namespaceName = qualifyResponsesNamespaceToolName(namespace, namespaceName)
	}
	children := make([]any, 0)
	for _, child := range anySlice(tool["tools"]) {
		children = append(children, normalizeNativeTool(child, namespaceName)...)
	}
	return children
}

func nativeResponsesHostedToolType(toolType string) bool {
	switch toolType {
	case "web_search", "web_search_preview", "x_search", "image_generation",
		"collections_search", "file_search", "code_execution", "code_interpreter",
		"mcp", "shell":
		return true
	default:
		return false
	}
}

func normalizeNativeFunctionParameters(tool map[string]any) {
	toolType := strings.TrimSpace(stringValue(tool["type"]))
	if toolType != "" && toolType != "function" {
		return
	}

	target := tool
	if function, ok := tool["function"].(map[string]any); ok {
		target = function
	}
	if strings.TrimSpace(stringValue(target["name"])) == "" {
		return
	}

	var parameters any
	for _, key := range []string{"parameters", "parametersJsonSchema", "input_schema"} {
		if value, exists := target[key]; exists {
			parameters = value
			break
		}
	}
	target["parameters"] = normalizeNativeObjectSchema(parameters)
	delete(target, "parametersJsonSchema")
	delete(target, "input_schema")
}

func normalizeNativeObjectSchema(value any) map[string]any {
	schema, ok := value.(map[string]any)
	if !ok || schema == nil {
		return safeNativeObjectSchema()
	}
	schema = cloneMap(schema)

	rootType, hasType := schema["type"]
	if !hasType {
		if branch := firstNativeObjectUnionBranch(schema); branch != nil {
			schema = mergeNativeObjectSchema(schema, branch)
		} else {
			schema["type"] = "object"
		}
	} else if !nativeSchemaTypeIsObjectOnly(rootType) {
		if branch := firstNativeObjectUnionBranch(schema); branch != nil {
			schema = mergeNativeObjectSchema(schema, branch)
		} else {
			return safeNativeObjectSchema()
		}
	} else {
		schema["type"] = "object"
		for _, unionName := range []string{"oneOf", "anyOf"} {
			branches, exists := schema[unionName].([]any)
			if !exists {
				continue
			}
			for _, rawBranch := range branches {
				branch, branchOK := rawBranch.(map[string]any)
				if !branchOK || !nativeSchemaTypeIsObjectOnly(branch["type"]) {
					if objectBranch := firstNativeObjectUnionBranch(schema); objectBranch != nil {
						schema = mergeNativeObjectSchema(schema, objectBranch)
					} else {
						return safeNativeObjectSchema()
					}
					break
				}
			}
		}
	}

	if _, ok := schema["properties"].(map[string]any); !ok {
		schema["properties"] = map[string]any{}
	}
	return schema
}

func nativeSchemaTypeIsObjectOnly(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "object")
	case []any:
		if len(typed) == 0 {
			return false
		}
		for _, item := range typed {
			text, ok := item.(string)
			if !ok || !strings.EqualFold(strings.TrimSpace(text), "object") {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func firstNativeObjectUnionBranch(schema map[string]any) map[string]any {
	for _, unionName := range []string{"oneOf", "anyOf"} {
		branches, ok := schema[unionName].([]any)
		if !ok {
			continue
		}
		for _, rawBranch := range branches {
			branch, branchOK := rawBranch.(map[string]any)
			if !branchOK {
				continue
			}
			branchType, hasType := branch["type"]
			if !hasType || nativeSchemaTypeIsObjectOnly(branchType) {
				return branch
			}
		}
	}
	return nil
}

func mergeNativeObjectSchema(root, branch map[string]any) map[string]any {
	merged := cloneMap(root)
	delete(merged, "oneOf")
	delete(merged, "anyOf")
	for key, value := range branch {
		merged[key] = value
	}
	merged["type"] = "object"
	return merged
}

func safeNativeObjectSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func normalizeNativeHistoryItem(item map[string]any) {
	itemType := strings.TrimSpace(stringValue(item["type"]))
	switch itemType {
	case "function_call", "custom_tool_call":
		name := strings.TrimSpace(stringValue(item["name"]))
		namespace := strings.TrimSpace(stringValue(item["namespace"]))
		if namespace != "" && name != "" {
			item["name"] = qualifyResponsesNamespaceToolName(namespace, name)
		}
		delete(item, "namespace")
		if itemType == "custom_tool_call" {
			arguments, _ := common.Marshal(map[string]any{"input": item["input"]})
			item["type"] = "function_call"
			item["arguments"] = string(arguments)
			delete(item, "input")
		}
		normalizeNativeFunctionCallID(item)
	case "custom_tool_call_output":
		item["type"] = "function_call_output"
		normalizeNativeFunctionCallID(item)
	case "tool_search_call":
		arguments, _ := common.Marshal(nativeObject(item["arguments"]))
		item["type"] = "function_call"
		item["name"] = "tool_search"
		item["arguments"] = string(arguments)
		delete(item, "execution")
		normalizeNativeFunctionCallID(item)
	}
}

func normalizeNativeFunctionCallID(item map[string]any) {
	id := strings.TrimSpace(stringValue(item["id"]))
	if id == "" || strings.HasPrefix(id, "fc_") {
		return
	}
	suffix := id
	if separator := strings.IndexByte(id, '_'); separator >= 0 && separator+1 < len(id) {
		suffix = id[separator+1:]
	}
	item["id"] = "fc_" + suffix
}

func normalizeNativeToolChoice(choice map[string]any) {
	name := strings.TrimSpace(stringValue(choice["name"]))
	namespace := strings.TrimSpace(stringValue(choice["namespace"]))
	if namespace != "" && name != "" {
		choice["name"] = qualifyResponsesNamespaceToolName(namespace, name)
		delete(choice, "namespace")
	}
	if allowed, ok := choice["tools"].([]any); ok {
		for _, value := range allowed {
			if tool, ok := value.(map[string]any); ok {
				normalizeNativeToolChoice(tool)
			}
		}
	}
}

func normalizeNativeToolChoicePolicy(tools []any, choice map[string]any) ([]any, any) {
	normalizeNativeToolChoice(choice)
	choiceType := strings.TrimSpace(stringValue(choice["type"]))
	if choiceType == "custom" {
		choice["type"] = "function"
		delete(choice, "format")
		choiceType = "function"
	}
	if choiceType != "allowed_tools" {
		return tools, choice
	}

	allowed := make(map[string]struct{})
	for _, value := range anySlice(choice["tools"]) {
		tool, ok := value.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(stringValue(tool["name"]))
		if name == "" {
			name = strings.TrimSpace(stringValue(tool["type"]))
		}
		if name != "" {
			allowed[name] = struct{}{}
		}
	}
	filtered := filterNativeToolsByName(tools, allowed)
	if len(filtered) == 0 {
		return nil, "none"
	}
	if strings.EqualFold(strings.TrimSpace(stringValue(choice["mode"])), "required") {
		return filtered, "required"
	}
	return filtered, "auto"
}

func filterNativeToolsByName(tools []any, allowed map[string]struct{}) []any {
	filtered := make([]any, 0, len(tools))
	for _, value := range tools {
		tool, ok := value.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(stringValue(tool["name"]))
		if name == "" {
			name = strings.TrimSpace(stringValue(tool["type"]))
		}
		if _, ok = allowed[name]; ok {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func dedupeNativeTools(tools []any) []any {
	seen := make(map[string]struct{}, len(tools))
	result := make([]any, 0, len(tools))
	for _, value := range tools {
		tool, ok := value.(map[string]any)
		if !ok {
			continue
		}
		key := strings.TrimSpace(nativeToolName(tool))
		if key == "" {
			key = "type:" + strings.TrimSpace(stringValue(tool["type"]))
		}
		if _, ok = seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func nativeToolSearchOutputText(tools []any, status string) string {
	names := make([]string, 0, len(tools))
	for _, value := range tools {
		if tool, ok := value.(map[string]any); ok {
			if name := strings.TrimSpace(nativeToolName(tool)); name != "" {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		return "Tool search loaded these tools. Call one by its exact name: " + strings.Join(names, ", ") + "."
	}
	if status != "" && !strings.EqualFold(status, "completed") && !strings.EqualFold(status, "success") {
		return "Tool search failed with status: " + status + "."
	}
	return "Tool search returned no tools."
}

func nativeObject(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		var object map[string]any
		if common.UnmarshalJsonStr(text, &object) == nil && object != nil {
			return object
		}
	}
	return map[string]any{}
}

func restoreNativeResponseObject(root map[string]any, originalRequest []byte, customTools map[string]struct{}) {
	for _, key := range []string{"item"} {
		if item, ok := root[key].(map[string]any); ok {
			restoreNativeResponseItem(item, originalRequest, customTools)
		}
	}
	for _, key := range []string{"output"} {
		if output, ok := root[key].([]any); ok {
			for _, value := range output {
				if item, ok := value.(map[string]any); ok {
					restoreNativeResponseItem(item, originalRequest, customTools)
				}
			}
		}
	}
	if response, ok := root["response"].(map[string]any); ok {
		restoreNativeResponseObject(response, originalRequest, customTools)
	}

	eventType := strings.TrimSpace(stringValue(root["type"]))
	if _, singleCustomTool := responsesSingleCustomToolName(originalRequest); singleCustomTool {
		switch eventType {
		case "response.function_call_arguments.delta":
			root["type"] = "response.custom_tool_call_input.delta"
		case "response.function_call_arguments.done":
			root["type"] = "response.custom_tool_call_input.done"
			if arguments, ok := root["arguments"]; ok {
				root["input"] = unwrapCustomToolInput(stringValue(arguments))
				delete(root, "arguments")
			}
		}
	}
}

func restoreNativeResponseItem(item map[string]any, originalRequest []byte, customTools map[string]struct{}) {
	itemType := strings.TrimSpace(stringValue(item["type"]))
	if itemType != "function_call" && itemType != "custom_tool_call" {
		return
	}
	qualifiedName := strings.TrimSpace(stringValue(item["name"]))
	if qualifiedName == "" {
		return
	}
	if itemType == "function_call" && responsesHasToolSearch(originalRequest) && qualifiedName == "tool_search" {
		arguments := make(map[string]any)
		if rawArguments := strings.TrimSpace(stringValue(item["arguments"])); rawArguments != "" {
			_ = common.UnmarshalJsonStr(rawArguments, &arguments)
		}
		item["type"] = "tool_search_call"
		item["execution"] = "client"
		item["arguments"] = arguments
		delete(item, "name")
		if id := strings.TrimSpace(stringValue(item["id"])); strings.HasPrefix(id, "fc_") {
			item["id"] = "tsc_" + strings.TrimPrefix(id, "fc_")
		}
		return
	}
	if itemType == "function_call" && responsesHasToolType(originalRequest, "web_search") && qualifiedName == "web_search" {
		arguments := nativeObject(item["arguments"])
		query := strings.TrimSpace(stringValue(arguments["query"]))
		queries := anySlice(arguments["queries"])
		if len(queries) == 0 && query != "" {
			queries = []any{query}
		}
		item["type"] = "web_search_call"
		item["action"] = map[string]any{"type": "search", "query": query, "queries": queries}
		delete(item, "name")
		delete(item, "arguments")
		delete(item, "call_id")
		if id := strings.TrimSpace(stringValue(item["id"])); strings.HasPrefix(id, "fc_") {
			item["id"] = "ws_" + strings.TrimPrefix(id, "fc_")
		}
		return
	}
	if _, ok := customTools[qualifiedName]; ok {
		item["type"] = "custom_tool_call"
		if arguments, exists := item["arguments"]; exists {
			item["input"] = unwrapCustomToolInput(stringValue(arguments))
			delete(item, "arguments")
		}
	}
	name, namespace := splitResponsesQualifiedFunctionCallFromRequest(originalRequest, qualifiedName)
	item["name"] = name
	if namespace != "" {
		item["namespace"] = namespace
	} else {
		delete(item, "namespace")
	}
}

func responsesHasToolSearch(requestRawJSON []byte) bool {
	return responsesHasToolType(requestRawJSON, "tool_search")
}

func responsesHasToolType(requestRawJSON []byte, wanted string) bool {
	found := false
	collect := func(tools any) {
		for _, value := range anySlice(tools) {
			tool, ok := value.(map[string]any)
			if ok && strings.TrimSpace(stringValue(tool["type"])) == wanted {
				found = true
				return
			}
		}
	}
	var request map[string]any
	if common.Unmarshal(requestRawJSON, &request) != nil {
		return false
	}
	collect(request["tools"])
	for _, value := range anySlice(request["input"]) {
		item, ok := value.(map[string]any)
		if !ok || strings.TrimSpace(stringValue(item["type"])) != "additional_tools" {
			continue
		}
		collect(item["tools"])
	}
	return found
}

func nativeToolName(tool map[string]any) string {
	if name := strings.TrimSpace(stringValue(tool["name"])); name != "" {
		return name
	}
	if function, ok := tool["function"].(map[string]any); ok {
		return strings.TrimSpace(stringValue(function["name"]))
	}
	return ""
}

func setNativeToolName(tool map[string]any, name string) {
	if function, ok := tool["function"].(map[string]any); ok && stringValue(tool["name"]) == "" {
		function["name"] = name
		return
	}
	tool["name"] = name
}

func anySlice(value any) []any {
	if values, ok := value.([]any); ok {
		return values
	}
	return nil
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func cloneMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
