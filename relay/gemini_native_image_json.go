package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
)

var nativeGeminiTopLevelAliases = map[string]string{
	"cached_content":     "cachedContent",
	"generation_config":  "generationConfig",
	"safety_settings":    "safetySettings",
	"system_instruction": "systemInstruction",
	"tool_config":        "toolConfig",
}

var nativeGeminiGenerationAliases = map[string]string{
	"candidate_count":               "candidateCount",
	"enable_enhanced_civic_answers": "enableEnhancedCivicAnswers",
	"frequency_penalty":             "frequencyPenalty",
	"image_config":                  "imageConfig",
	"logprobs":                      "logprobs",
	"max_output_tokens":             "maxOutputTokens",
	"media_resolution":              "mediaResolution",
	"presence_penalty":              "presencePenalty",
	"response_json_schema":          "responseJsonSchema",
	"response_logprobs":             "responseLogprobs",
	"response_mime_type":            "responseMimeType",
	"response_modalities":           "responseModalities",
	"response_schema":               "responseSchema",
	"speech_config":                 "speechConfig",
	"stop_sequences":                "stopSequences",
	"thinking_config":               "thinkingConfig",
	"top_k":                         "topK",
	"top_p":                         "topP",
}

var nativeGeminiPartAliases = map[string]string{
	"code_execution_result": "codeExecutionResult",
	"executable_code":       "executableCode",
	"file_data":             "fileData",
	"function_call":         "functionCall",
	"function_response":     "functionResponse",
	"inline_data":           "inlineData",
	"media_resolution":      "mediaResolution",
	"thought_signature":     "thoughtSignature",
	"video_metadata":        "videoMetadata",
}

func canonicalizeNativeGeminiImageJSON(input []byte) ([]byte, bool, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(input, &root); err != nil {
		return nil, false, fmt.Errorf("invalid Gemini JSON body: %w", err)
	}
	changed, err := canonicalizeRawAliases(root, nativeGeminiTopLevelAliases)
	if err != nil {
		return nil, false, err
	}

	if raw, ok := root["contents"]; ok {
		normalized, nestedChanged, nestedErr := canonicalizeGeminiContents(raw)
		if nestedErr != nil {
			return nil, false, nestedErr
		}
		if nestedChanged {
			root["contents"] = normalized
			changed = true
		}
	}
	if raw, ok := root["systemInstruction"]; ok {
		normalized, nestedChanged, nestedErr := canonicalizeGeminiContent(raw)
		if nestedErr != nil {
			return nil, false, nestedErr
		}
		if nestedChanged {
			root["systemInstruction"] = normalized
			changed = true
		}
	}
	if raw, ok := root["generationConfig"]; ok {
		normalized, nestedChanged, nestedErr := canonicalizeGeminiGenerationConfig(raw)
		if nestedErr != nil {
			return nil, false, nestedErr
		}
		if nestedChanged {
			root["generationConfig"] = normalized
			changed = true
		}
	}
	if !changed {
		return input, false, nil
	}
	output, err := json.Marshal(root)
	if err != nil {
		return nil, false, fmt.Errorf("marshal canonical Gemini JSON body: %w", err)
	}
	return output, true, nil
}

func canonicalizeRawAliases(object map[string]json.RawMessage, aliases map[string]string) (bool, error) {
	changed := false
	for alias, canonical := range aliases {
		aliasValue, exists := object[alias]
		if !exists {
			continue
		}
		if canonicalValue, duplicate := object[canonical]; duplicate && !bytes.Equal(bytes.TrimSpace(aliasValue), bytes.TrimSpace(canonicalValue)) {
			return false, fmt.Errorf("conflicting Gemini fields %q and %q", alias, canonical)
		}
		object[canonical] = aliasValue
		delete(object, alias)
		changed = true
	}
	return changed, nil
}

func canonicalizeGeminiContents(raw json.RawMessage) (json.RawMessage, bool, error) {
	var contents []json.RawMessage
	if err := json.Unmarshal(raw, &contents); err != nil {
		return nil, false, fmt.Errorf("invalid Gemini contents: %w", err)
	}
	changed := false
	for index, content := range contents {
		normalized, itemChanged, err := canonicalizeGeminiContent(content)
		if err != nil {
			return nil, false, err
		}
		if itemChanged {
			contents[index] = normalized
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}
	output, err := json.Marshal(contents)
	return output, true, err
}

func canonicalizeGeminiContent(raw json.RawMessage) (json.RawMessage, bool, error) {
	var content map[string]json.RawMessage
	if err := json.Unmarshal(raw, &content); err != nil {
		return nil, false, fmt.Errorf("invalid Gemini content: %w", err)
	}
	partsRaw, ok := content["parts"]
	if !ok {
		return raw, false, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(partsRaw, &parts); err != nil {
		return nil, false, fmt.Errorf("invalid Gemini parts: %w", err)
	}
	changed := false
	for index, partRaw := range parts {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(partRaw, &part); err != nil {
			return nil, false, fmt.Errorf("invalid Gemini part: %w", err)
		}
		partChanged, err := canonicalizeRawAliases(part, nativeGeminiPartAliases)
		if err != nil {
			return nil, false, err
		}
		if inlineRaw, exists := part["inlineData"]; exists {
			var inline map[string]json.RawMessage
			if err := json.Unmarshal(inlineRaw, &inline); err != nil {
				return nil, false, fmt.Errorf("invalid Gemini inlineData: %w", err)
			}
			inlineChanged, inlineErr := canonicalizeRawAliases(inline, map[string]string{"mime_type": "mimeType"})
			if inlineErr != nil {
				return nil, false, inlineErr
			}
			if inlineChanged {
				part["inlineData"], _ = json.Marshal(inline)
				partChanged = true
			}
		}
		if partChanged {
			parts[index], _ = json.Marshal(part)
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}
	content["parts"], _ = json.Marshal(parts)
	output, err := json.Marshal(content)
	return output, true, err
}

func canonicalizeGeminiGenerationConfig(raw json.RawMessage) (json.RawMessage, bool, error) {
	var config map[string]json.RawMessage
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, false, fmt.Errorf("invalid Gemini generationConfig: %w", err)
	}
	changed, err := canonicalizeRawAliases(config, nativeGeminiGenerationAliases)
	if err != nil {
		return nil, false, err
	}
	if imageRaw, ok := config["imageConfig"]; ok {
		var imageConfig map[string]json.RawMessage
		if err := json.Unmarshal(imageRaw, &imageConfig); err != nil {
			return nil, false, fmt.Errorf("invalid Gemini imageConfig: %w", err)
		}
		imageChanged, imageErr := canonicalizeRawAliases(imageConfig, map[string]string{
			"aspect_ratio": "aspectRatio",
			"image_size":   "imageSize",
		})
		if imageErr != nil {
			return nil, false, imageErr
		}
		if imageChanged {
			config["imageConfig"], _ = json.Marshal(imageConfig)
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}
	output, err := json.Marshal(config)
	return output, true, err
}
