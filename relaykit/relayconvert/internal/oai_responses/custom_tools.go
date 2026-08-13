package oairesponses

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

// LowerCustomToolsForFunctionProvider converts Responses free-form custom
// tools to function tools for upstream protocols that only support JSON-schema
// functions. The request-scoped conversion metadata lets the response path
// restore the original custom_tool_call contract.
func LowerCustomToolsForFunctionProvider(request dto.OpenAIResponsesRequest, info convmeta.Meta) (dto.OpenAIResponsesRequest, error) {
	customNames := make(map[string]struct{})
	if RawJSONPresent(request.Tools) {
		var tools []map[string]any
		if err := kitutil.Unmarshal(request.Tools, &tools); err != nil {
			return request, fmt.Errorf("invalid tools: %w", err)
		}
		for index, tool := range tools {
			if strings.TrimSpace(kitutil.Interface2String(tool["type"])) != "custom" {
				continue
			}
			name := strings.TrimSpace(kitutil.Interface2String(tool["name"]))
			if name == "" {
				return request, fmt.Errorf("custom tool at index %d is missing name", index)
			}
			customNames[name] = struct{}{}
			if info != nil {
				info.MarkResponsesCustomTool(name)
			}
			tools[index] = map[string]any{
				"type":        "function",
				"name":        name,
				"description": kitutil.Interface2String(tool["description"]),
				"parameters": map[string]any{
					"type":                 "object",
					"properties":           map[string]any{"input": map[string]any{"type": "string"}},
					"required":             []string{"input"},
					"additionalProperties": false,
				},
			}
		}
		encoded, err := kitutil.Marshal(tools)
		if err != nil {
			return request, err
		}
		request.Tools = encoded
	}

	if !RawJSONPresent(request.Input) || kitutil.GetJsonType(request.Input) != "array" {
		return request, nil
	}
	var items []map[string]any
	if err := kitutil.Unmarshal(request.Input, &items); err != nil {
		return request, fmt.Errorf("invalid input: %w", err)
	}
	customCallIDs := make(map[string]struct{})
	for index, item := range items {
		if strings.TrimSpace(kitutil.Interface2String(item["type"])) != ResponsesInputTypeCustomToolCall {
			continue
		}
		name := strings.TrimSpace(kitutil.Interface2String(item["name"]))
		if name != "" {
			customNames[name] = struct{}{}
			if info != nil {
				info.MarkResponsesCustomTool(name)
			}
		}
		callID := strings.TrimSpace(kitutil.Interface2String(item["call_id"]))
		if callID != "" {
			customCallIDs[callID] = struct{}{}
		}
		arguments, err := kitutil.Marshal(map[string]any{"input": kitutil.Interface2String(item["input"])})
		if err != nil {
			return request, err
		}
		items[index] = map[string]any{
			"type":      ResponsesInputTypeFunctionCall,
			"call_id":   callID,
			"name":      name,
			"arguments": string(arguments),
		}
	}
	for index, item := range items {
		if strings.TrimSpace(kitutil.Interface2String(item["type"])) != ResponsesInputTypeCustomToolOutput {
			continue
		}
		callID := strings.TrimSpace(kitutil.Interface2String(item["call_id"]))
		if _, ok := customCallIDs[callID]; !ok {
			continue
		}
		items[index]["type"] = ResponsesInputTypeFunctionCallOutput
	}
	encoded, err := kitutil.Marshal(items)
	if err != nil {
		return request, err
	}
	request.Input = encoded
	return request, nil
}
