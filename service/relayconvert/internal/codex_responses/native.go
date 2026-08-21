package codexresponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/tidwall/gjson"
)

type nativeToolIdentity struct {
	name      string
	namespace string
}

// NativeCompatibility contains the small, immutable lookup tables needed to
// restore Codex namespace/custom-tool semantics on response events. Building
// it once avoids rescanning the complete request for every SSE event.
type NativeCompatibility struct {
	customTools      map[string]struct{}
	toolIdentities   map[string]nativeToolIdentity
	singleCustomTool bool
}

// OpenAIResponsesRequestNeedsNormalization reports whether a Codex request
// contains only the provider-native Responses shapes. It inspects only the
// small tool/control fields; the potentially huge conversation input is not
// decoded unless it contains Codex-only namespace markers.
func OpenAIResponsesRequestNeedsNormalization(request *dto.OpenAIResponsesRequest) bool {
	if request == nil {
		return false
	}
	if bytes.Contains(request.Input, []byte(`"additional_tools"`)) || bytes.Contains(request.Input, []byte(`"namespace"`)) {
		return true
	}
	if bytes.Contains(request.ToolChoice, []byte(`"namespace"`)) {
		return true
	}
	if len(request.Tools) == 0 || string(request.Tools) == "null" {
		return false
	}

	needsNormalization := false
	root := gjson.ParseBytes(request.Tools)
	if !root.IsArray() {
		return true
	}
	root.ForEach(func(_, tool gjson.Result) bool {
		toolType := strings.TrimSpace(tool.Get("type").String())
		if toolType == "namespace" || tool.Get("namespace").Exists() {
			needsNormalization = true
			return false
		}
		if toolType != "" && toolType != "function" {
			return true
		}
		target := tool
		if function := tool.Get("function"); function.Exists() {
			target = function
		}
		if target.Get("parametersJsonSchema").Exists() || target.Get("input_schema").Exists() {
			needsNormalization = true
			return false
		}
		parameters := target.Get("parameters")
		if !parameters.Exists() || !parameters.IsObject() || !strings.EqualFold(strings.TrimSpace(parameters.Get("type").String()), "object") || parameters.Get("oneOf").Exists() || parameters.Get("anyOf").Exists() {
			needsNormalization = true
			return false
		}
		return true
	})
	return needsNormalization
}

// PrepareOpenAIResponsesRequest normalizes only the Responses fields that
// differ between Codex Desktop and provider-native Responses. The request has
// already been decoded by NewAPI, so this does not parse or serialize the
// complete request a second time.
func PrepareOpenAIResponsesRequest(request *dto.OpenAIResponsesRequest) (*NativeCompatibility, error) {
	if request == nil {
		return nil, fmt.Errorf("nil Responses request")
	}
	compat := &NativeCompatibility{
		customTools:    make(map[string]struct{}),
		toolIdentities: make(map[string]nativeToolIdentity),
	}

	tools := make([]any, 0)
	appendTools := func(value any) {
		compat.collectToolMetadata(value, "")
		for _, tool := range anySlice(value) {
			tools = append(tools, normalizeNativeTool(tool, "")...)
		}
	}

	if len(request.Tools) > 0 && string(request.Tools) != "null" {
		var topLevelTools any
		if err := json.Unmarshal(request.Tools, &topLevelTools); err != nil {
			return nil, fmt.Errorf("invalid Responses tools: %w", err)
		}
		appendTools(topLevelTools)
	}

	inputNeedsNormalization := bytes.Contains(request.Input, []byte(`"additional_tools"`)) || bytes.Contains(request.Input, []byte(`"namespace"`))
	if inputNeedsNormalization && len(request.Input) > 0 && string(request.Input) != "null" && firstJSONByte(request.Input) == '[' {
		var input []any
		if err := json.Unmarshal(request.Input, &input); err != nil {
			return nil, fmt.Errorf("invalid Responses input: %w", err)
		}
		normalizedInput := make([]any, 0, len(input))
		for _, value := range input {
			item, ok := value.(map[string]any)
			if !ok {
				normalizedInput = append(normalizedInput, value)
				continue
			}
			if strings.TrimSpace(stringValue(item["type"])) == "additional_tools" {
				appendTools(item["tools"])
				continue
			}
			normalizeNativeHistoryItem(item)
			normalizedInput = append(normalizedInput, item)
		}
		encoded, err := json.Marshal(normalizedInput)
		if err != nil {
			return nil, err
		}
		request.Input = encoded
	}

	if len(tools) > 0 {
		encoded, err := json.Marshal(tools)
		if err != nil {
			return nil, err
		}
		request.Tools = encoded
	} else {
		request.Tools = nil
	}

	if len(request.ToolChoice) > 0 && string(request.ToolChoice) != "null" && firstJSONByte(request.ToolChoice) == '{' {
		var choice map[string]any
		if err := json.Unmarshal(request.ToolChoice, &choice); err != nil {
			return nil, fmt.Errorf("invalid Responses tool_choice: %w", err)
		}
		normalizeNativeToolChoice(choice)
		encoded, err := json.Marshal(choice)
		if err != nil {
			return nil, err
		}
		request.ToolChoice = encoded
	}

	compat.singleCustomTool = len(compat.customTools) == 1 && len(compat.toolIdentities) == 1
	return compat, nil
}

func firstJSONByte(raw []byte) byte {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return b
		}
	}
	return 0
}

func (compat *NativeCompatibility) collectToolMetadata(value any, namespace string) {
	for _, rawTool := range anySlice(value) {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		toolType := strings.TrimSpace(stringValue(tool["type"]))
		if toolType == "namespace" {
			nestedNamespace := strings.TrimSpace(stringValue(tool["name"]))
			if namespace != "" {
				nestedNamespace = qualifyResponsesNamespaceToolName(namespace, nestedNamespace)
			}
			compat.collectToolMetadata(tool["tools"], nestedNamespace)
			continue
		}
		name := nativeToolName(tool)
		if name == "" {
			continue
		}
		qualified := qualifyResponsesNamespaceToolName(namespace, name)
		compat.toolIdentities[qualified] = nativeToolIdentity{name: name, namespace: namespace}
		if toolType == "custom" {
			compat.customTools[qualified] = struct{}{}
		}
	}
}

// RestorePayload restores one provider response object or one SSE data object
// using metadata prepared once for the request.
func (compat *NativeCompatibility) RestorePayload(rawJSON []byte) ([]byte, error) {
	if compat == nil {
		return rawJSON, nil
	}
	// Text/reasoning deltas are the overwhelming majority of a stream. They do
	// not carry tool identity fields, so return their original bytes without a
	// JSON decode/encode round trip.
	toolPayload := bytes.Contains(rawJSON, []byte(`"function_call"`)) || bytes.Contains(rawJSON, []byte(`"custom_tool_call"`))
	customDelta := compat.singleCustomTool && bytes.Contains(rawJSON, []byte(`"response.function_call_arguments`))
	if !toolPayload && !customDelta {
		return rawJSON, nil
	}
	var root map[string]any
	if err := json.Unmarshal(rawJSON, &root); err != nil {
		return nil, err
	}
	compat.restoreResponseObject(root)
	return json.Marshal(root)
}

func (compat *NativeCompatibility) restoreResponseObject(root map[string]any) {
	if item, ok := root["item"].(map[string]any); ok {
		compat.restoreResponseItem(item)
	}
	if output, ok := root["output"].([]any); ok {
		for _, value := range output {
			if item, ok := value.(map[string]any); ok {
				compat.restoreResponseItem(item)
			}
		}
	}
	if response, ok := root["response"].(map[string]any); ok {
		compat.restoreResponseObject(response)
	}

	if compat.singleCustomTool {
		switch strings.TrimSpace(stringValue(root["type"])) {
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

func (compat *NativeCompatibility) restoreResponseItem(item map[string]any) {
	itemType := strings.TrimSpace(stringValue(item["type"]))
	if itemType != "function_call" && itemType != "custom_tool_call" {
		return
	}
	qualifiedName := strings.TrimSpace(stringValue(item["name"]))
	if qualifiedName == "" {
		return
	}
	if _, ok := compat.customTools[qualifiedName]; ok {
		item["type"] = "custom_tool_call"
		if arguments, exists := item["arguments"]; exists {
			item["input"] = unwrapCustomToolInput(stringValue(arguments))
			delete(item, "arguments")
		}
	}
	identity, ok := compat.toolIdentities[qualifiedName]
	if !ok {
		delete(item, "namespace")
		return
	}
	item["name"] = identity.name
	if identity.namespace != "" {
		item["namespace"] = identity.namespace
	} else {
		delete(item, "namespace")
	}
}

// NormalizeOpenAIResponsesRequest keeps the Responses protocol intact while
// flattening Codex namespace declarations into provider-safe tool names.
func NormalizeOpenAIResponsesRequest(rawJSON []byte) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(rawJSON, &root); err != nil {
		return nil, fmt.Errorf("invalid Responses request: %w", err)
	}

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
			if strings.TrimSpace(stringValue(item["type"])) == "additional_tools" {
				appendTools(item["tools"])
				continue
			}
			normalizeNativeHistoryItem(item)
			normalizedInput = append(normalizedInput, item)
		}
		root["input"] = normalizedInput
	}

	if len(tools) > 0 {
		root["tools"] = tools
	} else {
		delete(root, "tools")
	}
	if choice, ok := root["tool_choice"].(map[string]any); ok {
		normalizeNativeToolChoice(choice)
	}

	return json.Marshal(root)
}

// RestoreOpenAIResponsesPayload restores Codex namespace and custom-tool
// semantics on a native Responses JSON object or SSE event payload.
func RestoreOpenAIResponsesPayload(originalRequest, rawJSON []byte) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(rawJSON, &root); err != nil {
		return nil, err
	}
	customTools := responsesCustomToolNames(originalRequest)
	restoreNativeResponseObject(root, originalRequest, customTools)
	return json.Marshal(root)
}

func normalizeNativeTool(value any, namespace string) []any {
	tool, ok := value.(map[string]any)
	if !ok {
		return []any{value}
	}
	toolType := strings.TrimSpace(stringValue(tool["type"]))
	if toolType != "namespace" {
		copyTool := cloneMap(tool)
		name := nativeToolName(copyTool)
		if namespace != "" && name != "" {
			setNativeToolName(copyTool, qualifyResponsesNamespaceToolName(namespace, name))
		}
		normalizeNativeFunctionParameters(copyTool)
		delete(copyTool, "namespace")
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
	if itemType != "function_call" && itemType != "custom_tool_call" {
		return
	}
	name := strings.TrimSpace(stringValue(item["name"]))
	namespace := strings.TrimSpace(stringValue(item["namespace"]))
	if namespace != "" && name != "" {
		item["name"] = qualifyResponsesNamespaceToolName(namespace, name)
	}
	delete(item, "namespace")
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
