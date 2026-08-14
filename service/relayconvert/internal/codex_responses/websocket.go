package codexresponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	websocketRequestTypeCreate = "response.create"
	websocketRequestTypeAppend = "response.append"
)

// WebsocketState retains only the transcript needed to translate Codex's
// incremental websocket protocol into self-contained Responses requests.
// It deliberately contains no credentials, channel state, or billing state.
type WebsocketState struct {
	lastRequest  []byte
	lastOutput   []json.RawMessage
	lastResponse string
}

type WebsocketStateSnapshot struct {
	lastRequest  []byte
	lastOutput   []json.RawMessage
	lastResponse string
}

func (s *WebsocketState) Snapshot() WebsocketStateSnapshot {
	return WebsocketStateSnapshot{
		lastRequest:  bytes.Clone(s.lastRequest),
		lastOutput:   cloneRawMessages(s.lastOutput),
		lastResponse: s.lastResponse,
	}
}

func (s *WebsocketState) Restore(snapshot WebsocketStateSnapshot) {
	s.lastRequest = bytes.Clone(snapshot.lastRequest)
	s.lastOutput = cloneRawMessages(snapshot.lastOutput)
	s.lastResponse = snapshot.lastResponse
}

// NormalizeWebsocketRequest implements Codex's HTTP-fallback websocket model:
// response.append is rebuilt as a normal, self-contained Responses request so
// the existing New API relay remains the sole channel and billing authority.
func (s *WebsocketState) NormalizeWebsocketRequest(payload []byte, maxStateBytes int) ([]byte, error) {
	if !json.Valid(payload) {
		return nil, fmt.Errorf("invalid websocket request JSON")
	}

	var request map[string]json.RawMessage
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, fmt.Errorf("invalid websocket request: %w", err)
	}
	requestType, err := rawJSONString(request["type"])
	if err != nil {
		return nil, fmt.Errorf("invalid websocket request type")
	}
	switch requestType {
	case websocketRequestTypeCreate:
	case websocketRequestTypeAppend:
		if len(s.lastRequest) == 0 {
			return nil, fmt.Errorf("websocket request received before response.create")
		}
	default:
		return nil, fmt.Errorf("unsupported websocket request type: %s", requestType)
	}

	delete(request, "type")
	request["stream"] = json.RawMessage("true")
	if len(s.lastRequest) == 0 {
		if previous, exists := request["previous_response_id"]; exists {
			if value, previousErr := rawJSONString(previous); previousErr == nil && strings.TrimSpace(value) != "" {
				return nil, fmt.Errorf("previous response is not available on this websocket; resend the full conversation input")
			}
		}
		modelName, modelErr := rawJSONString(request["model"])
		if modelErr != nil || strings.TrimSpace(modelName) == "" {
			return nil, fmt.Errorf("missing model in response.create request")
		}
		if _, exists := request["input"]; !exists {
			request["input"] = json.RawMessage("[]")
		}
		return s.commitRequest(request, maxStateBytes)
	}

	var previous map[string]json.RawMessage
	if err = json.Unmarshal(s.lastRequest, &previous); err != nil {
		return nil, fmt.Errorf("invalid websocket session state")
	}
	inheritWebsocketRequestFields(request, previous)

	nextInput, err := rawJSONArray(request["input"])
	if err != nil {
		return nil, fmt.Errorf("websocket request requires array field: input")
	}

	if shouldReplaceWebsocketTranscript(requestType, request, nextInput) {
		delete(request, "previous_response_id")
		request["input"] = mustMarshalRawMessages(nextInput)
		return s.commitRequest(request, maxStateBytes)
	}

	existingInput, err := rawJSONArray(previous["input"])
	if err != nil {
		return nil, fmt.Errorf("invalid previous websocket input")
	}
	appendInput := removeCompactionItems(nextInput)
	merged := make([]json.RawMessage, 0, len(existingInput)+len(s.lastOutput)+len(appendInput))
	merged = append(merged, cloneRawMessages(existingInput)...)
	merged = append(merged, cloneRawMessages(s.lastOutput)...)
	merged = append(merged, cloneRawMessages(appendInput)...)
	merged = dedupeWebsocketInput(merged)

	delete(request, "previous_response_id")
	request["input"] = mustMarshalRawMessages(merged)
	return s.commitRequest(request, maxStateBytes)
}

func (s *WebsocketState) HasRequest() bool {
	return len(s.lastRequest) > 0
}

// ObserveWebsocketEvent records the completed response output required by the
// next response.append. Delta payloads are never retained.
func (s *WebsocketState) ObserveWebsocketEvent(payload []byte, outputItems map[int]json.RawMessage, fallback *[]json.RawMessage) bool {
	var event struct {
		Type        string          `json:"type"`
		OutputIndex *int            `json:"output_index"`
		Item        json.RawMessage `json:"item"`
		Response    struct {
			ID     string            `json:"id"`
			Output []json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return false
	}
	if event.Type == "response.output_item.done" && len(event.Item) > 0 {
		item := bytes.Clone(event.Item)
		if event.OutputIndex != nil {
			outputItems[*event.OutputIndex] = item
		} else {
			*fallback = append(*fallback, item)
		}
	}
	if event.Type != "response.completed" && event.Type != "response.done" {
		return false
	}

	s.lastResponse = event.Response.ID
	if len(event.Response.Output) > 0 {
		s.lastOutput = cloneRawMessages(event.Response.Output)
	} else {
		s.lastOutput = orderedWebsocketOutput(outputItems, *fallback)
	}
	return true
}

func (s *WebsocketState) StateBytes() int {
	total := len(s.lastRequest) + len(s.lastResponse)
	for _, item := range s.lastOutput {
		total += len(item)
	}
	return total
}

func (s *WebsocketState) commitRequest(request map[string]json.RawMessage, maxStateBytes int) ([]byte, error) {
	normalized, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to normalize websocket request: %w", err)
	}
	if maxStateBytes > 0 && len(normalized) > maxStateBytes {
		return nil, fmt.Errorf("websocket conversation state exceeds %d bytes; compact or start a new session", maxStateBytes)
	}
	s.lastRequest = bytes.Clone(normalized)
	return normalized, nil
}

func inheritWebsocketRequestFields(next map[string]json.RawMessage, previous map[string]json.RawMessage) {
	for _, key := range []string{"model", "instructions", "tools", "tool_choice", "parallel_tool_calls", "reasoning", "text", "include"} {
		if _, exists := next[key]; exists {
			continue
		}
		if value, exists := previous[key]; exists {
			next[key] = bytes.Clone(value)
		}
	}
}

func shouldReplaceWebsocketTranscript(requestType string, request map[string]json.RawMessage, input []json.RawMessage) bool {
	if previous, exists := request["previous_response_id"]; exists {
		if value, err := rawJSONString(previous); err == nil && strings.TrimSpace(value) != "" {
			return false
		}
	}
	if requestType == websocketRequestTypeCreate && containsCodexLocalCompactionSummary(input) {
		return true
	}
	for _, item := range input {
		var metadata struct {
			Type string `json:"type"`
			Role string `json:"role"`
		}
		if json.Unmarshal(item, &metadata) != nil {
			continue
		}
		if metadata.Type == "function_call" || metadata.Type == "custom_tool_call" {
			return true
		}
		if metadata.Type == "message" && metadata.Role == "assistant" {
			return true
		}
	}
	return false
}

func containsCodexLocalCompactionSummary(input []json.RawMessage) bool {
	const prefix = "Another language model started to solve this problem and produced a summary of its thinking process."
	for _, item := range input {
		var message struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}
		if json.Unmarshal(item, &message) != nil || message.Role != "user" {
			continue
		}
		switch content := message.Content.(type) {
		case string:
			if strings.HasPrefix(content, prefix) {
				return true
			}
		case []any:
			for _, part := range content {
				object, ok := part.(map[string]any)
				if ok && object["type"] == "input_text" {
					if text, ok := object["text"].(string); ok && strings.HasPrefix(text, prefix) {
						return true
					}
				}
			}
		}
	}
	return false
}

func removeCompactionItems(input []json.RawMessage) []json.RawMessage {
	filtered := make([]json.RawMessage, 0, len(input))
	for _, item := range input {
		var metadata struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(item, &metadata) == nil && (metadata.Type == "compaction" || metadata.Type == "compaction_summary") {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func dedupeWebsocketInput(input []json.RawMessage) []json.RawMessage {
	lastByID := make(map[string]int)
	seenCallID := make(map[string]struct{})
	keep := make([]bool, len(input))
	for index, item := range input {
		var metadata struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			CallID string `json:"call_id"`
		}
		if json.Unmarshal(item, &metadata) != nil {
			keep[index] = true
			continue
		}
		if metadata.Type == "function_call" || metadata.Type == "custom_tool_call" {
			if metadata.CallID != "" {
				if _, exists := seenCallID[metadata.CallID]; exists {
					continue
				}
				seenCallID[metadata.CallID] = struct{}{}
			}
		}
		keep[index] = true
		if metadata.ID != "" {
			if previous, exists := lastByID[metadata.ID]; exists {
				keep[previous] = false
			}
			lastByID[metadata.ID] = index
		}
	}
	result := make([]json.RawMessage, 0, len(input))
	for index, item := range input {
		if keep[index] {
			result = append(result, item)
		}
	}
	return result
}

func orderedWebsocketOutput(indexed map[int]json.RawMessage, fallback []json.RawMessage) []json.RawMessage {
	result := make([]json.RawMessage, 0, len(indexed)+len(fallback))
	for index := 0; len(indexed) > 0; index++ {
		item, exists := indexed[index]
		if !exists {
			if index > len(indexed)+len(fallback)+1024 {
				break
			}
			continue
		}
		result = append(result, bytes.Clone(item))
		delete(indexed, index)
	}
	for _, item := range fallback {
		result = append(result, bytes.Clone(item))
	}
	return result
}

func rawJSONString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("missing string")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func rawJSONArray(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("missing array")
	}
	var value []json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func mustMarshalRawMessages(items []json.RawMessage) json.RawMessage {
	value, _ := json.Marshal(items)
	return value
}

func cloneRawMessages(items []json.RawMessage) []json.RawMessage {
	cloned := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, bytes.Clone(item))
	}
	return cloned
}
