package codexresponses

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// responsesToolDeclaration is one Responses tool declaration paired with the
// Chat Completions function name it produces. Keeping one canonical walk for
// request conversion and reverse conversion prevents the two directions from
// disagreeing when top-level tools and additional_tools overlap.
type responsesToolDeclaration struct {
	tool      gjson.Result
	chatName  string
	localName string
	namespace string
	custom    bool
}

func walkResponsesToolDeclarations(root gjson.Result, visit func(responsesToolDeclaration) bool) {
	proceed := true
	emit := func(tool gjson.Result, namespaceName string) {
		if !proceed {
			return
		}
		var custom bool
		switch strings.TrimSpace(tool.Get("type").String()) {
		case "", "function":
		case "custom":
			custom = true
		default:
			return
		}
		localName := responsesToolName(tool)
		if localName == "" {
			return
		}
		proceed = visit(responsesToolDeclaration{
			tool:      tool,
			chatName:  qualifyResponsesNamespaceToolName(namespaceName, localName),
			localName: localName,
			namespace: namespaceName,
			custom:    custom,
		})
	}
	scan := func(tools gjson.Result) {
		if !proceed || !tools.Exists() || !tools.IsArray() {
			return
		}
		tools.ForEach(func(_, tool gjson.Result) bool {
			if strings.TrimSpace(tool.Get("type").String()) == "namespace" {
				if children := tool.Get("tools"); children.Exists() && children.IsArray() {
					namespaceName := strings.TrimSpace(tool.Get("name").String())
					children.ForEach(func(_, child gjson.Result) bool {
						emit(child, namespaceName)
						return proceed
					})
				}
				return proceed
			}
			emit(tool, "")
			return proceed
		})
	}

	scan(root.Get("tools"))
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				scan(item.Get("tools"))
			}
			return proceed
		})
	}
}

func mergeResponsesRequestChatTools(root gjson.Result) [][]byte {
	var merged [][]byte
	seenToolNames := make(map[string]struct{})
	walkResponsesToolDeclarations(root, func(declaration responsesToolDeclaration) bool {
		if _, duplicate := seenToolNames[declaration.chatName]; duplicate {
			return true
		}
		convert := convertResponsesFunctionToolToOpenAIChat
		if declaration.custom {
			convert = convertResponsesCustomToolToOpenAIChat
		}
		if chatTool, ok := convert(declaration.tool, declaration.chatName); ok {
			seenToolNames[declaration.chatName] = struct{}{}
			merged = append(merged, chatTool)
		}
		return true
	})
	return merged
}

func convertResponsesToolToOpenAIChatTools(tool gjson.Result) [][]byte {
	toolType := strings.TrimSpace(tool.Get("type").String())
	switch toolType {
	case "", "function":
		if tJSON, ok := convertResponsesFunctionToolToOpenAIChat(tool, ""); ok {
			return [][]byte{tJSON}
		}
	case "namespace":
		return convertResponsesNamespaceToolToOpenAIChat(tool)
	case "custom":
		if tJSON, ok := convertResponsesCustomToolToOpenAIChat(tool, ""); ok {
			return [][]byte{tJSON}
		}
	default:
		return nil
	}
	return nil
}

// convertResponsesCustomToolToOpenAIChat maps a Responses freeform ("custom")
// tool onto a Chat Completions function tool with a single freeform "input"
// string, mirroring the function-based shape Codex uses for apply_patch.
func convertResponsesCustomToolToOpenAIChat(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(tool)
	}
	if name == "" {
		return nil, false
	}
	chatTool := []byte(`{"type":"function","function":{"name":"","description":"","parameters":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}}}`)
	chatTool, _ = sjson.SetBytes(chatTool, "function.name", name)
	if description := responsesToolDescription(tool); description != "" {
		chatTool, _ = sjson.SetBytes(chatTool, "function.description", description)
	}
	return chatTool, true
}

func convertResponsesNamespaceToolToOpenAIChat(tool gjson.Result) [][]byte {
	namespaceName := strings.TrimSpace(tool.Get("name").String())
	children := tool.Get("tools")
	if !children.Exists() || !children.IsArray() {
		return nil
	}

	var out [][]byte
	children.ForEach(func(_, child gjson.Result) bool {
		childName := responsesToolName(child)
		qualifiedName := qualifyResponsesNamespaceToolName(namespaceName, childName)
		switch strings.TrimSpace(child.Get("type").String()) {
		case "", "function":
			if tJSON, ok := convertResponsesFunctionToolToOpenAIChat(child, qualifiedName); ok {
				out = append(out, tJSON)
			}
		case "custom":
			if tJSON, ok := convertResponsesCustomToolToOpenAIChat(child, qualifiedName); ok {
				out = append(out, tJSON)
			}
		}
		return true
	})
	return out
}

func convertResponsesFunctionToolToOpenAIChat(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(tool)
	}
	if name == "" {
		return nil, false
	}

	chatTool := []byte(`{"type":"function","function":{"name":"","description":"","parameters":{}}}`)
	chatTool, _ = sjson.SetBytes(chatTool, "function.name", name)
	if description := responsesToolDescription(tool); description != "" {
		chatTool, _ = sjson.SetBytes(chatTool, "function.description", description)
	}
	if parameters := responsesToolParameters(tool); parameters.Exists() {
		chatTool, _ = sjson.SetRawBytes(chatTool, "function.parameters", []byte(parameters.Raw))
	}
	return chatTool, true
}

func responsesToolName(tool gjson.Result) string {
	if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
		return name
	}
	return strings.TrimSpace(tool.Get("function.name").String())
}

func responsesToolDescription(tool gjson.Result) string {
	if description := tool.Get("description").String(); description != "" {
		return description
	}
	return tool.Get("function.description").String()
}

func responsesToolParameters(tool gjson.Result) gjson.Result {
	for _, path := range []string{
		"parameters",
		"parametersJsonSchema",
		"input_schema",
		"function.parameters",
		"function.parametersJsonSchema",
	} {
		if parameters := tool.Get(path); parameters.Exists() {
			return parameters
		}
	}
	return gjson.Result{}
}

// responsesToolOutputText flattens a tool output value that may be a plain
// string or an array of content parts ({"type":"input_text","text":...}) into
// a single text payload for a Chat Completions tool message.
func responsesToolOutputText(output gjson.Result) string {
	if output.Type == gjson.String {
		return output.String()
	}
	if output.IsArray() {
		var b strings.Builder
		output.ForEach(func(_, part gjson.Result) bool {
			if part.Type == gjson.String {
				b.WriteString(part.String())
				return true
			}
			if text := part.Get("text"); text.Exists() {
				b.WriteString(text.String())
			}
			return true
		})
		return b.String()
	}
	if output.Exists() {
		return output.Raw
	}
	return ""
}

// responsesCustomToolNames follows the same first-declaration-wins rule as the
// merged Chat tool list. A discarded duplicate custom declaration therefore
// cannot change how a surviving ordinary function call is restored.
func responsesCustomToolNames(requestRawJSON []byte) map[string]struct{} {
	names := make(map[string]struct{})
	seenToolNames := make(map[string]struct{})
	walkResponsesToolDeclarations(gjson.ParseBytes(requestRawJSON), func(declaration responsesToolDeclaration) bool {
		if _, duplicate := seenToolNames[declaration.chatName]; duplicate {
			return true
		}
		seenToolNames[declaration.chatName] = struct{}{}
		if declaration.custom {
			names[declaration.chatName] = struct{}{}
		}
		return true
	})
	return names
}

func responsesSingleCustomToolName(requestRawJSON []byte) (string, bool) {
	customToolNames := responsesCustomToolNames(requestRawJSON)
	if len(customToolNames) != 1 {
		return "", false
	}

	toolCount := len(mergeResponsesRequestChatTools(gjson.ParseBytes(requestRawJSON)))
	for name := range customToolNames {
		return name, toolCount == 1
	}
	return "", false
}

// unwrapCustomToolInput extracts the freeform input from the {"input": "..."}
// function-call arguments produced for a converted custom tool; it falls back
// to the raw arguments when the wrapper is absent.
func unwrapCustomToolInput(arguments string) string {
	if v := gjson.Get(arguments, "input"); v.Exists() {
		if v.Type == gjson.String {
			return v.String()
		}
		return v.Raw
	}
	return arguments
}

func qualifyResponsesNamespaceToolName(namespaceName, childName string) string {
	childName = strings.TrimSpace(childName)
	if childName == "" || namespaceName == "" || strings.HasPrefix(childName, "mcp__") {
		return childName
	}
	if strings.HasPrefix(childName, namespaceName) {
		return childName
	}
	if strings.HasSuffix(namespaceName, "__") {
		return namespaceName + childName
	}
	return namespaceName + "__" + childName
}

func splitResponsesQualifiedFunctionCallFromRequest(requestRawJSON []byte, qualifiedName string) (name, namespace string) {
	qualifiedName = strings.TrimSpace(qualifiedName)
	if qualifiedName == "" {
		return "", ""
	}

	var resolvedName string
	var resolvedNamespace string
	found := false
	walkResponsesToolDeclarations(gjson.ParseBytes(requestRawJSON), func(declaration responsesToolDeclaration) bool {
		if declaration.chatName != qualifiedName {
			return true
		}
		resolvedName = declaration.localName
		resolvedNamespace = declaration.namespace
		found = true
		return false
	})
	if found {
		return resolvedName, resolvedNamespace
	}
	return qualifiedName, ""
}

func pickRequestJSON(originalRequestRawJSON, requestRawJSON []byte) []byte {
	if len(originalRequestRawJSON) > 0 && gjson.ValidBytes(originalRequestRawJSON) {
		return originalRequestRawJSON
	}
	if len(requestRawJSON) > 0 && gjson.ValidBytes(requestRawJSON) {
		return requestRawJSON
	}
	return nil
}

func applyResponsesFunctionCallNamespaceFields(item []byte, requestRawJSON []byte, qualifiedName string, itemPath string) []byte {
	name, namespace := splitResponsesQualifiedFunctionCallFromRequest(requestRawJSON, qualifiedName)
	namePath := "name"
	namespacePath := "namespace"
	if itemPath != "" {
		namePath = itemPath + ".name"
		namespacePath = itemPath + ".namespace"
	}
	item, _ = sjson.SetBytes(item, namePath, name)
	if namespace != "" {
		item, _ = sjson.SetBytes(item, namespacePath, namespace)
	} else {
		item, _ = sjson.DeleteBytes(item, namespacePath)
	}
	return item
}
