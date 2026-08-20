package codexresponses

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/tidwall/gjson"
)

func TestPrepareOpenAIResponsesRequestKeepsPlainInputBuiltinsAndSummaryBytes(t *testing.T) {
	input := json.RawMessage(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"请根据参考内容总结，不要乱码。"}]}]`)
	request := &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: input,
		Tools: json.RawMessage(`[
			{"type":"web_search","search_context_size":"high"},
			{"type":"image_generation","action":"generate","model":"gpt-image-2"}
		]`),
	}
	inputAddress := &request.Input[0]

	compat, err := PrepareOpenAIResponsesRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(request.Input, input) {
		t.Fatalf("plain input changed: %s", request.Input)
	}
	if &request.Input[0] != inputAddress {
		t.Fatal("plain input was reallocated")
	}
	if got := gjson.GetBytes(request.Tools, "0.type").String(); got != "web_search" {
		t.Fatalf("web search tool = %q", got)
	}
	if got := gjson.GetBytes(request.Tools, "1.type").String(); got != "image_generation" {
		t.Fatalf("image generation tool = %q", got)
	}

	summary := []byte(`{"type":"response.output_text.delta","delta":"中文总结保持正常"}`)
	restored, err := compat.RestorePayload(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, summary) || &restored[0] != &summary[0] {
		t.Fatalf("plain summary event was decoded or rewritten: %s", restored)
	}
	image := []byte(`{"type":"response.output_item.done","item":{"type":"image_generation_call","status":"completed"}}`)
	restored, err = compat.RestorePayload(image)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, image) || &restored[0] != &image[0] {
		t.Fatalf("image event was decoded or rewritten: %s", restored)
	}
}

func TestNormalizeOpenAIResponsesRequestKeepsNativeFieldsAndFlattensNamespaces(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"previous_response_id":"resp_previous",
		"reasoning":{"effort":"high","summary":"auto"},
		"input":[
			{"role":"user","content":"hello"},
			{"type":"function_call","call_id":"call_1","name":"get_me","namespace":"mcp__github","arguments":"{}"},
			{"type":"additional_tools","tools":[{"type":"namespace","name":"terminal","tools":[{"type":"custom","name":"exec","description":"Run command"}]}]}
		],
		"tools":[
			{"type":"web_search","search_context_size":"high"},
			{"type":"namespace","name":"mcp__github","tools":[{"type":"function","name":"get_me","parameters":{"type":"object"}}]}
		],
		"tool_choice":{"type":"allowed_tools","tools":[{"type":"function","name":"get_me","namespace":"mcp__github"}]}
	}`)

	out, err := NormalizeOpenAIResponsesRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, "previous_response_id").String(); got != "resp_previous" {
		t.Fatalf("previous_response_id = %q", got)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning effort = %q", got)
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("tools.0.type = %q", got)
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "mcp__github__get_me" {
		t.Fatalf("tools.1.name = %q", got)
	}
	if got := gjson.GetBytes(out, "tools.2.name").String(); got != "terminal__exec" {
		t.Fatalf("tools.2.name = %q", got)
	}
	if got := gjson.GetBytes(out, "input.#").Int(); got != 2 {
		t.Fatalf("input count = %d", got)
	}
	if got := gjson.GetBytes(out, "input.1.name").String(); got != "mcp__github__get_me" {
		t.Fatalf("history function name = %q", got)
	}
	if got := gjson.GetBytes(out, "tool_choice.tools.0.name").String(); got != "mcp__github__get_me" {
		t.Fatalf("allowed tool name = %q", got)
	}
}

func TestNormalizeOpenAIResponsesRequestRepairsCodexToolSchemas(t *testing.T) {
	raw := []byte(`{
		"model":"deepseek-v4-flash",
		"input":"weather",
		"tools":[
			{"type":"namespace","name":"codex_app","tools":[
				{"type":"function","name":"automation_update","parameters":null},
				{"type":"function","name":"union_tool","parameters":{"oneOf":[{"type":"object","properties":{"action":{"type":"string"}},"required":["action"]},{"type":"null"}]}},
				{"type":"function","name":"lookup","parametersJsonSchema":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}
			]}
		]
	}`)

	out, err := NormalizeOpenAIResponsesRequest(raw)
	if err != nil {
		t.Fatalf("NormalizeOpenAIResponsesRequest() error = %v", err)
	}

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "codex_app__automation_update" {
		t.Fatalf("automation tool name = %q; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.parameters.type").String(); got != "object" {
		t.Fatalf("automation parameters type = %q, want object; output=%s", got, out)
	}
	if !gjson.GetBytes(out, "tools.0.parameters.properties").IsObject() {
		t.Fatalf("automation properties are not an object; output=%s", out)
	}
	if got := gjson.GetBytes(out, "tools.1.parameters.properties.action.type").String(); got != "string" {
		t.Fatalf("union object branch was not preserved; output=%s", out)
	}
	if got := gjson.GetBytes(out, "tools.1.parameters.required.0").String(); got != "action" {
		t.Fatalf("union required field was not preserved; output=%s", out)
	}
	if gjson.GetBytes(out, "tools.1.parameters.oneOf").Exists() {
		t.Fatalf("nullable root union was not removed; output=%s", out)
	}
	if got := gjson.GetBytes(out, "tools.2.parameters.properties.query.type").String(); got != "string" {
		t.Fatalf("valid parametersJsonSchema was not preserved; output=%s", out)
	}
	if gjson.GetBytes(out, "tools.2.parametersJsonSchema").Exists() {
		t.Fatalf("parametersJsonSchema alias was not canonicalized; output=%s", out)
	}
}

func TestRestoreOpenAIResponsesPayloadRestoresNamespaceAndCustomTool(t *testing.T) {
	request := []byte(`{
		"tools":[
			{"type":"namespace","name":"mcp__github","tools":[{"type":"function","name":"get_me","parameters":{"type":"object"}}]},
			{"type":"namespace","name":"terminal","tools":[{"type":"custom","name":"exec"}]}
		]
	}`)
	response := []byte(`{
		"id":"resp_1","object":"response","status":"completed",
		"output":[
			{"type":"function_call","call_id":"call_1","name":"mcp__github__get_me","arguments":"{}"},
			{"type":"function_call","call_id":"call_2","name":"terminal__exec","arguments":"{\"input\":\"dir\"}"}
		]
	}`)

	out, err := RestoreOpenAIResponsesPayload(request, response)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, "output.0.name").String(); got != "get_me" {
		t.Fatalf("function name = %q", got)
	}
	if got := gjson.GetBytes(out, "output.0.namespace").String(); got != "mcp__github" {
		t.Fatalf("function namespace = %q", got)
	}
	if got := gjson.GetBytes(out, "output.1.type").String(); got != "custom_tool_call" {
		t.Fatalf("custom type = %q", got)
	}
	if got := gjson.GetBytes(out, "output.1.name").String(); got != "exec" {
		t.Fatalf("custom name = %q", got)
	}
	if got := gjson.GetBytes(out, "output.1.namespace").String(); got != "terminal" {
		t.Fatalf("custom namespace = %q", got)
	}
	if got := gjson.GetBytes(out, "output.1.input").String(); got != "dir" {
		t.Fatalf("custom input = %q", got)
	}
}

func TestRestoreOpenAIResponsesPayloadMapsSingleCustomToolDelta(t *testing.T) {
	request := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`)
	payload := []byte(`{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"input\":\"patch\"}"}`)

	out, err := RestoreOpenAIResponsesPayload(request, payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, "type").String(); got != "response.custom_tool_call_input.done" {
		t.Fatalf("event type = %q", got)
	}
	if got := gjson.GetBytes(out, "input").String(); got != "patch" {
		t.Fatalf("input = %q", got)
	}
}

func BenchmarkCodexNormalizationLargePlainInput(b *testing.B) {
	input, err := json.Marshal([]map[string]any{{
		"type":    "message",
		"role":    "user",
		"content": []map[string]any{{"type": "input_text", "text": strings.Repeat("x", 4<<20)}},
	}})
	if err != nil {
		b.Fatal(err)
	}
	raw, err := json.Marshal(&dto.OpenAIResponsesRequest{Model: "gpt-test", Input: input})
	if err != nil {
		b.Fatal(err)
	}

	b.Run("legacy-whole-body", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := NormalizeOpenAIResponsesRequest(raw); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("single-layer-field-aware", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			request := &dto.OpenAIResponsesRequest{Model: "gpt-test", Input: input}
			if _, err := PrepareOpenAIResponsesRequest(request); err != nil {
				b.Fatal(err)
			}
		}
	})
}
