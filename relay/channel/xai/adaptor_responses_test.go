package xai

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestMapsCodexCustomToolForXAI(t *testing.T) {
	request := dto.OpenAIResponsesRequest{
		Model:      "grok-4.5",
		Tools:      json.RawMessage(`[{"type":"custom","name":"shell_command","description":"Run a command","format":{"type":"text"}}]`),
		ToolChoice: json.RawMessage(`{"type":"custom","name":"shell_command"}`),
	}
	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, request)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(payload, "tools.0.type").String(); got != "function" {
		t.Fatalf("tool type = %q, want function; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "tools.0.name").String(); got != "shell_command" {
		t.Fatalf("tool name = %q, want shell_command", got)
	}
	if got := gjson.GetBytes(payload, "tools.0.parameters.properties.input.type").String(); got != "string" {
		t.Fatalf("input schema type = %q, want string", got)
	}
	if gjson.GetBytes(payload, "tools.0.format").Exists() {
		t.Fatalf("custom format must not be forwarded to xAI: %s", payload)
	}
	if got := gjson.GetBytes(payload, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool choice type = %q, want function", got)
	}
}

func TestConvertOpenAIResponsesRequestMapsCodexCustomHistoryForXAI(t *testing.T) {
	request := dto.OpenAIResponsesRequest{
		Model: "grok-4.5",
		Input: json.RawMessage(`[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]},
			{"type":"custom_tool_call","call_id":"call_1","name":"shell_command","input":"Get-ChildItem -Force"},
			{"type":"custom_tool_call_output","call_id":"call_1","output":"ok"}
		]`),
	}
	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, request)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(payload, "input.1.type").String(); got != "function_call" {
		t.Fatalf("history call type = %q, want function_call; payload=%s", got, payload)
	}
	arguments := gjson.GetBytes(payload, "input.1.arguments").String()
	if got := gjson.Get(arguments, "input").String(); got != "Get-ChildItem -Force" {
		t.Fatalf("wrapped custom input = %q", got)
	}
	if gjson.GetBytes(payload, "input.1.input").Exists() {
		t.Fatalf("custom input must be replaced by arguments: %s", payload)
	}
	if got := gjson.GetBytes(payload, "input.2.type").String(); got != "function_call_output" {
		t.Fatalf("history output type = %q, want function_call_output", got)
	}
}

func TestConvertOpenAIResponsesRequestPreservesXAINativeTools(t *testing.T) {
	tools := json.RawMessage(`[
		{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}},
		{"type":"web_search"},
		{"type":"x_search"},
		{"type":"image_generation"},
		{"type":"mcp","server_url":"https://example.test/mcp"}
	]`)
	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{Model: "grok-4.5", Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	convertedRequest := converted.(dto.OpenAIResponsesRequest)
	var want, got any
	if err = json.Unmarshal(tools, &want); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(convertedRequest.Tools, &got); err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("native xAI tools changed:\nwant %s\n got %s", wantJSON, gotJSON)
	}
}

func TestXAIConvertedFunctionCallRestoresCodexCustomTool(t *testing.T) {
	original := []byte(`{"model":"grok-4.5","tools":[{"type":"custom","name":"shell_command"}]}`)
	xaiResponse := []byte(`{"id":"resp_1","output":[{"type":"function_call","call_id":"call_1","name":"shell_command","arguments":"{\"input\":\"Get-ChildItem\"}"}]}`)
	restored, err := relayconvert.RestoreCodexResponsesPayload(original, xaiResponse)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(restored, "output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("restored type = %q, want custom_tool_call; payload=%s", got, restored)
	}
	if got := gjson.GetBytes(restored, "output.0.input").String(); got != "Get-ChildItem" {
		t.Fatalf("restored input = %q, want Get-ChildItem", got)
	}
}
