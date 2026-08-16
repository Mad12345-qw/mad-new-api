package codexresponses

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

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
	if gjson.GetBytes(out, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must be removed by the stateless converter: %s", out)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning effort = %q", got)
	}
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 2 {
		t.Fatalf("tools count = %d", got)
	}
	if got := gjson.GetBytes(out, "tools.1.type").String(); got != "namespace" {
		t.Fatalf("tools.1.type = %q", got)
	}
	if got := gjson.GetBytes(out, "input.#").Int(); got != 3 {
		t.Fatalf("input count = %d", got)
	}
	if got := gjson.GetBytes(out, "input.1.name").String(); got != "get_me" {
		t.Fatalf("history function name = %q", got)
	}
	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "allowed_tools" {
		t.Fatalf("tool_choice.type = %q", got)
	}
}

func TestNormalizeOpenAIResponsesRequestPreservesNativeCustomHistoryIDs(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5.6-luna",
		"previous_response_id":"resp_previous",
		"input":[
			{"type":"custom_tool_call","id":"ctco_01a003bf-75c1-76f0-bc53-46aef06da266","call_id":"call_custom","name":"apply_patch","input":"patch"},
			{"type":"custom_tool_call_output","call_id":"call_custom","output":"ok"},
			{"type":"tool_search_call","id":"tsc_search","call_id":"call_search","execution":"client","arguments":{"query":"search"},"status":"completed"}
		]
	}`)

	out, err := NormalizeOpenAIResponsesRequest(raw)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(out, "previous_response_id").Exists())
	require.Equal(t, "custom_tool_call", gjson.GetBytes(out, "input.0.type").String())
	require.Equal(t, "ctco_01a003bf-75c1-76f0-bc53-46aef06da266", gjson.GetBytes(out, "input.0.id").String())
	require.Equal(t, "custom_tool_call_output", gjson.GetBytes(out, "input.1.type").String())
	require.Equal(t, "tool_search_call", gjson.GetBytes(out, "input.2.type").String())
	require.Equal(t, "tsc_search", gjson.GetBytes(out, "input.2.id").String())
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

	out, err := NormalizeOpenAIResponsesRequestForProvider(raw)
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

func TestNormalizeOpenAIResponsesRequestForChatProviderPreservesReasoningOwnership(t *testing.T) {
	raw := []byte(`{
		"input":[
			{"type":"message","role":"assistant","phase":"commentary","reasoning_content":"message reasoning","content":"checking"},
			{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}","reasoning_content":"call reasoning"},
			{"type":"custom_tool_call","call_id":"call_2","name":"patch","input":"change","reasoning_content":"custom reasoning"}
		]
	}`)

	generic, err := NormalizeOpenAIResponsesRequestForProvider(raw)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(generic, "input.0.phase").Exists())
	require.False(t, gjson.GetBytes(generic, "input.0.reasoning_content").Exists())
	require.False(t, gjson.GetBytes(generic, "input.1.reasoning_content").Exists())
	require.False(t, gjson.GetBytes(generic, "input.2.reasoning_content").Exists())

	chat, err := NormalizeOpenAIResponsesRequestForChatProvider(raw)
	require.NoError(t, err)
	require.Equal(t, "commentary", gjson.GetBytes(chat, "input.0.phase").String())
	require.Equal(t, "message reasoning", gjson.GetBytes(chat, "input.0.reasoning_content").String())
	require.Equal(t, "call reasoning", gjson.GetBytes(chat, "input.1.reasoning_content").String())
	require.Equal(t, "custom reasoning", gjson.GetBytes(chat, "input.2.reasoning_content").String())
}

func TestNormalizeOpenAIResponsesRequestForChatProviderKeepsReasoningBeforeToolReplay(t *testing.T) {
	raw := []byte(`{
		"input":[
			{
				"type":"reasoning",
				"id":"rs_040f959e93f05f58016a816b7518c88199bb517f9db4030f71",
				"summary":[{"type":"summary_text","text":"verify before calling the tool"}],
				"encrypted_content":"provider-owned"
			},
			{"type":"function_call","id":"fc_long_history_id","call_id":"call_1","name":"lookup","arguments":"{}"},
			{"type":"function_call_output","id":"fco_long_history_id","call_id":"call_1","output":"ok"}
		]
	}`)

	normalized, err := NormalizeOpenAIResponsesRequestForChatProvider(raw)
	require.NoError(t, err)
	require.Equal(t, "reasoning", gjson.GetBytes(normalized, "input.0.type").String())
	require.Equal(t, "verify before calling the tool", gjson.GetBytes(normalized, "input.0.summary.0.text").String())

	converted := ConvertCodexResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", normalized, false)
	require.Equal(t, "verify before calling the tool", gjson.GetBytes(converted, "messages.0.reasoning_content").String())
	require.Equal(t, "call_1", gjson.GetBytes(converted, "messages.0.tool_calls.0.id").String())
	require.Equal(t, "call_1", gjson.GetBytes(converted, "messages.1.tool_call_id").String())
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

func TestRestoreOpenAIResponsesPayloadMapsToolSearchFunctionCall(t *testing.T) {
	request := []byte(`{"tools":[{"type":"tool_search"}]}`)
	response := []byte(`{
		"type":"response.output_item.done",
		"output_index":0,
		"item":{"id":"fc_call_1","type":"function_call","status":"completed","call_id":"call_1","name":"tool_search","arguments":"{\"query\":\"agent tools\",\"limit\":3}"}
	}`)

	out, err := RestoreOpenAIResponsesPayload(request, response)
	require.NoError(t, err)
	require.Equal(t, "tool_search_call", gjson.GetBytes(out, "item.type").String())
	require.Equal(t, "tsc_call_1", gjson.GetBytes(out, "item.id").String())
	require.Equal(t, "client", gjson.GetBytes(out, "item.execution").String())
	require.Equal(t, "agent tools", gjson.GetBytes(out, "item.arguments.query").String())
	require.Equal(t, int64(3), gjson.GetBytes(out, "item.arguments.limit").Int())
	require.False(t, gjson.GetBytes(out, "item.name").Exists())
}

func TestRestoreOpenAIResponsesPayloadMapsHostedWebSearchFunctionCall(t *testing.T) {
	request := []byte(`{"tools":[{"type":"web_search","search_context_size":"high"}]}`)
	response := []byte(`{
		"type":"response.output_item.done",
		"output_index":0,
		"item":{"id":"fc_web_1","type":"function_call","status":"completed","call_id":"call_web_1","name":"web_search","arguments":"{\"query\":\"latest release\"}"}
	}`)

	out, err := RestoreOpenAIResponsesPayload(request, response)
	require.NoError(t, err)
	require.Equal(t, "web_search_call", gjson.GetBytes(out, "item.type").String())
	require.Equal(t, "ws_web_1", gjson.GetBytes(out, "item.id").String())
	require.Equal(t, "search", gjson.GetBytes(out, "item.action.type").String())
	require.Equal(t, "latest release", gjson.GetBytes(out, "item.action.queries.0").String())
	require.False(t, gjson.GetBytes(out, "item.name").Exists())
	require.False(t, gjson.GetBytes(out, "item.call_id").Exists())
}

func TestNormalizeOpenAIResponsesRequestBuildsOneProviderSafeToolContract(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"input":[
			{"role":"user","content":"use tools and search"},
			{"type":"custom_tool_call","id":"ctco_01a003bf-75c1-76f0-bc53-46aef06da266","call_id":"call_custom","name":"apply_patch","input":"*** Begin Patch"},
			{"type":"custom_tool_call_output","call_id":"call_custom","output":"ok"},
			{"type":"tool_search_call","id":"tsc_1","call_id":"call_search","execution":"client","arguments":{"query":"agent tools","limit":3},"status":"completed"},
			{"type":"tool_search_output","id":"tso_01a00547-dd34-7080-97b7-8c91164b78de","call_id":"call_search","status":"completed","tools":[{"type":"namespace","name":"agents","tools":[{"type":"function","name":"spawn","parameters":{"type":"object"}}]}]},
			{"type":"additional_tools","tools":[{"type":"namespace","name":"terminal","tools":[{"type":"custom","name":"exec"}]}]}
		],
		"tools":[
			{"type":"web_search","search_context_size":"high"},
			{"type":"custom","name":"apply_patch","format":{"type":"text"}},
			{"type":"tool_search"},
			{"type":"namespace","name":"mcp__demo","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}
		]
	}`)

	out, err := NormalizeOpenAIResponsesRequestForProvider(raw)
	require.NoError(t, err)

	require.Equal(t, int64(6), gjson.GetBytes(out, "tools.#").Int())
	assert.Equal(t, "web_search", gjson.GetBytes(out, "tools.0.type").String())
	assert.Equal(t, "function", gjson.GetBytes(out, "tools.1.type").String())
	assert.Equal(t, "apply_patch", gjson.GetBytes(out, "tools.1.name").String())
	assert.Equal(t, "string", gjson.GetBytes(out, "tools.1.parameters.properties.input.type").String())
	assert.Equal(t, "function", gjson.GetBytes(out, "tools.2.type").String())
	assert.Equal(t, "tool_search", gjson.GetBytes(out, "tools.2.name").String())
	assert.Equal(t, "mcp__demo__lookup", gjson.GetBytes(out, "tools.3.name").String())
	assert.Equal(t, "agents__spawn", gjson.GetBytes(out, "tools.4.name").String())
	assert.Equal(t, "terminal__exec", gjson.GetBytes(out, "tools.5.name").String())

	require.Equal(t, int64(5), gjson.GetBytes(out, "input.#").Int())
	assert.Equal(t, "function_call", gjson.GetBytes(out, "input.1.type").String())
	assert.Equal(t, "fc_01a003bf-75c1-76f0-bc53-46aef06da266", gjson.GetBytes(out, "input.1.id").String())
	assert.Equal(t, "*** Begin Patch", gjson.Get(gjson.GetBytes(out, "input.1.arguments").String(), "input").String())
	assert.Equal(t, "function_call_output", gjson.GetBytes(out, "input.2.type").String())
	assert.Equal(t, "function_call", gjson.GetBytes(out, "input.3.type").String())
	assert.Equal(t, "fc_1", gjson.GetBytes(out, "input.3.id").String())
	assert.Equal(t, "tool_search", gjson.GetBytes(out, "input.3.name").String())
	assert.Equal(t, "agent tools", gjson.Get(gjson.GetBytes(out, "input.3.arguments").String(), "query").String())
	assert.Equal(t, "function_call_output", gjson.GetBytes(out, "input.4.type").String())
	assert.False(t, gjson.GetBytes(out, "input.4.id").Exists())
	assert.Equal(t, "call_search", gjson.GetBytes(out, "input.4.call_id").String())
	assert.Contains(t, gjson.GetBytes(out, "input.4.output").String(), "agents__spawn")
	assert.NotContains(t, string(out), `"type":"custom"`)
	assert.NotContains(t, string(out), `"type":"tool_search"`)
	assert.NotContains(t, string(out), `"type":"namespace"`)
}

func TestNormalizeOpenAIResponsesRequestMapsAllowedToolsOnce(t *testing.T) {
	raw := []byte(`{
		"tools":[
			{"type":"function","name":"lookup","parameters":{"type":"object"}},
			{"type":"tool_search"},
			{"type":"web_search"}
		],
		"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"tool_search"},{"type":"function","name":"lookup"}]}
	}`)

	out, err := NormalizeOpenAIResponsesRequestForProvider(raw)
	require.NoError(t, err)
	require.Equal(t, int64(2), gjson.GetBytes(out, "tools.#").Int())
	assert.Equal(t, "lookup", gjson.GetBytes(out, "tools.0.name").String())
	assert.Equal(t, "tool_search", gjson.GetBytes(out, "tools.1.name").String())
	assert.Equal(t, "required", gjson.GetBytes(out, "tool_choice").String())
}

func TestNormalizeOpenAIResponsesRequestDoesNotGuessDeepSeekChannelSemanticsFromModelName(t *testing.T) {
	raw := []byte(`{
		"model":"deepseek-v4-flash",
		"tools":[{"type":"tool_search"}],
		"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"tool_search"}]}
	}`)

	out, err := NormalizeOpenAIResponsesRequestForProvider(raw)
	require.NoError(t, err)
	require.Equal(t, int64(1), gjson.GetBytes(out, "tools.#").Int())
	assert.Equal(t, "tool_search", gjson.GetBytes(out, "tools.0.name").String())
	assert.Equal(t, "required", gjson.GetBytes(out, "tool_choice").String())
}

func TestNormalizeOpenAIResponsesRequestDoesNotGuessXAIChannelSemanticsFromModelName(t *testing.T) {
	raw := []byte(`{
		"model":"grok-4.7",
		"input":"probe",
		"tools":[
			{"type":"function","name":"matrix_probe","parameters":{"type":"object"}},
			{"type":"function","name":"other","parameters":{"type":"object"}}
		],
		"tool_choice":{"type":"function","name":"matrix_probe"}
	}`)

	out, err := NormalizeOpenAIResponsesRequestForProvider(raw)
	require.NoError(t, err)
	require.Equal(t, "function", gjson.GetBytes(out, "tool_choice.type").String())
	require.Equal(t, "matrix_probe", gjson.GetBytes(out, "tool_choice.name").String())
	require.Equal(t, int64(2), gjson.GetBytes(out, "tools.#").Int())
	require.Equal(t, "matrix_probe", gjson.GetBytes(out, "tools.0.name").String())
}

func TestNormalizeOpenAIResponsesRequestMatchesOpenCodexUnknownToolAndCollisionRules(t *testing.T) {
	raw := []byte(`{
		"tools":[
			{"type":"function","name":"lookup","description":"top level","parameters":{"type":"object"}},
			{"type":"custom","name":"lookup"},
			{"type":"future_named_tool","name":"future","parameters":{"type":"object"}},
			{"type":"future_unnamed_tool"},
			{"type":"shell","name":"shell"},
			{"type":"namespace","name":"demo","tools":[{"type":"web_search","name":"invalid_nested"}]}
		]
	}`)

	out, err := NormalizeOpenAIResponsesRequestForProvider(raw)
	require.NoError(t, err)
	require.Equal(t, int64(3), gjson.GetBytes(out, "tools.#").Int())
	require.Equal(t, "function", gjson.GetBytes(out, "tools.0.type").String())
	require.Equal(t, "top level", gjson.GetBytes(out, "tools.0.description").String())
	require.Equal(t, "function", gjson.GetBytes(out, "tools.1.type").String())
	require.Equal(t, "future", gjson.GetBytes(out, "tools.1.name").String())
	require.Equal(t, "shell", gjson.GetBytes(out, "tools.2.type").String())
}
