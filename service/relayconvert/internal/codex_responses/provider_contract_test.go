package codexresponses

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIResponsesRequestForContractPreservesNativeAndChatReasoningOwnership(t *testing.T) {
	raw := []byte(`{"input":[{"type":"message","role":"assistant","phase":"commentary","content":"checking"},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}","reasoning_content":"owned reasoning"}]}`)

	for _, contract := range []ProviderContract{ProviderContractCodex, ProviderContractDeepSeek, ProviderContractMoonshot} {
		out, err := NormalizeOpenAIResponsesRequestForContract(raw, contract)
		require.NoError(t, err)
		require.Equal(t, "commentary", gjson.GetBytes(out, "input.0.phase").String(), string(contract))
		require.Equal(t, "owned reasoning", gjson.GetBytes(out, "input.1.reasoning_content").String(), string(contract))
	}
	for _, contract := range []ProviderContract{ProviderContractOpenAI, ProviderContractClaude, ProviderContractGemini, ProviderContractOpenAICompat} {
		out, err := NormalizeOpenAIResponsesRequestForContract(raw, contract)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(out, "input.0.phase").Exists(), string(contract))
		require.False(t, gjson.GetBytes(out, "input.1.reasoning_content").Exists(), string(contract))
	}
}

func TestEnsureOpenAINativeSearchToolFieldsUsesOnlyOpenAIModelContracts(t *testing.T) {
	tests := []struct {
		model    string
		contract ProviderContract
		want     bool
	}{
		{model: "gpt-5.6-luna", contract: ProviderContractOpenAI, want: true},
		{model: "gpt-5.7", contract: ProviderContractCodex, want: true},
		{model: "gpt-5.7", contract: ProviderContractXAI, want: false},
		{model: "grok-4.7", contract: ProviderContractOpenAI, want: false},
		{model: "gpt-image-2", contract: ProviderContractOpenAI, want: false},
	}

	for _, test := range tests {
		t.Run(test.model+"_"+string(test.contract), func(t *testing.T) {
			tools, _, err := EnsureOpenAINativeSearchToolFieldsForContract(test.model, []byte(`[{"type":"tool_search"}]`), nil, test.contract)
			require.NoError(t, err)
			if test.want {
				require.Equal(t, "web_search", gjson.GetBytes(tools, "0.type").String())
				require.Equal(t, "tool_search", gjson.GetBytes(tools, "1.type").String())
			} else {
				require.Equal(t, "tool_search", gjson.GetBytes(tools, "0.type").String())
				require.False(t, gjson.GetBytes(tools, `#(type=="web_search")`).Exists())
			}
		})
	}
}

func TestEnsureOpenAINativeSearchToolFieldsUpdatesAllowedToolsWithoutDuplication(t *testing.T) {
	tools, choice, err := EnsureOpenAINativeSearchToolFieldsForContract(
		"gpt-5.6-terra",
		[]byte(`[{"type":"tool_search"}]`),
		[]byte(`{"type":"allowed_tools","mode":"auto","tools":[{"type":"tool_search"}]}`),
		ProviderContractOpenAI,
	)
	require.NoError(t, err)
	require.Len(t, gjson.GetBytes(tools, `#(type=="web_search")#`).Array(), 1)
	require.Equal(t, "web_search", gjson.GetBytes(choice, "tools.1.type").String())

	tools, _, err = EnsureOpenAINativeSearchToolFieldsForContract(
		"gpt-5.6-terra",
		[]byte(`[{"type":"web_search"},{"type":"tool_search"}]`),
		nil,
		ProviderContractOpenAI,
	)
	require.NoError(t, err)
	require.Len(t, gjson.GetBytes(tools, `#(type=="web_search")#`).Array(), 1)
}

func TestNativeCodexContractPreservesHostedToolsAndSanitizesHistoryIDs(t *testing.T) {
	longReasoningID := "rs_" + strings.Repeat("a", 64)
	raw := []byte(`{
		"model":"gpt-5.6-luna",
		"previous_response_id":"resp_previous",
		"tools":[
			{"type":"web_search_preview"},
			{"type":"tool_search"},
			{"type":"custom","name":"apply_patch"},
			{"type":"namespace","name":"terminal","tools":[{"type":"custom","name":"exec"}]}
		],
		"input":[
			{"type":"custom_tool_call","id":"ctco_existing","call_id":"call_custom","name":"apply_patch","input":"patch"},
			{"type":"custom_tool_call_output","id":"item_output","call_id":"call_custom","output":"ok"},
			{"type":"function_call","id":"item_function","call_id":"call_function","name":"lookup","arguments":"{}"},
			{"type":"reasoning","id":"` + longReasoningID + `","encrypted_content":"encrypted","summary":[]}
		]
	}`)

	out, err := NormalizeOpenAIResponsesRequestForContract(raw, ProviderContractCodex)
	require.NoError(t, err)

	assert.False(t, gjson.GetBytes(out, "previous_response_id").Exists())
	assert.Equal(t, "web_search", gjson.GetBytes(out, "tools.0.type").String())
	assert.Equal(t, "tool_search", gjson.GetBytes(out, "tools.1.type").String())
	assert.Equal(t, "custom", gjson.GetBytes(out, "tools.2.type").String())
	assert.Equal(t, "namespace", gjson.GetBytes(out, "tools.3.type").String())
	assert.Equal(t, "ctc_ctco_existing", gjson.GetBytes(out, "input.0.id").String())
	assert.Equal(t, "ctco_item_output", gjson.GetBytes(out, "input.1.id").String())
	assert.Equal(t, "fc_item_function", gjson.GetBytes(out, "input.2.id").String())
	assert.Equal(t, int64(3), gjson.GetBytes(out, "input.#").Int())
}

func TestOpenAIContractPreservesHostedSearchAndConvertsCodexClientTools(t *testing.T) {
	longSearchID := "ws_" + strings.Repeat("b", 80)
	longReasoningID := "tco_" + strings.Repeat("c", 80)
	raw := []byte(`{
		"model":"gpt-5.6-luna",
		"previous_response_id":"resp_previous",
		"tools":[
			{"type":"web_search"},
			{"type":"tool_search"},
			{"type":"custom","name":"apply_patch"},
			{"type":"namespace","name":"terminal","tools":[{"type":"custom","name":"exec"}]}
		],
		"input":[
			{"type":"message","id":"message_commentary","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"checking"}]},
			{"type":"custom_tool_call","id":"ctco_existing","call_id":"call_custom","name":"apply_patch","input":"patch"},
			{"type":"custom_tool_call_output","call_id":"call_custom","output":"ok"},
			{"type":"tool_search_call","id":"tsc_search","call_id":"call_search","arguments":{"query":"tools"}},
			{"type":"reasoning","id":"` + longReasoningID + `","status":"completed","quality":"full","content":null},
			{"type":"web_search_call","id":"` + longSearchID + `","status":"completed","action":{"type":"search","query":"current facts"}},
			{"type":"function_call","id":"toolu_provider","call_id":"call_provider","name":"lookup","namespace":"audit","arguments":"{}","content":[],"quality":"full","role":"assistant","size":123,"status":"completed"}
		]
	}`)

	out, err := NormalizeOpenAIResponsesRequestForContract(raw, ProviderContractOpenAI)
	require.NoError(t, err)

	assert.False(t, gjson.GetBytes(out, "previous_response_id").Exists())
	assert.Equal(t, "web_search", gjson.GetBytes(out, "tools.0.type").String())
	assert.Equal(t, "function", gjson.GetBytes(out, "tools.1.type").String())
	assert.Equal(t, "tool_search", gjson.GetBytes(out, "tools.1.name").String())
	assert.Equal(t, "function", gjson.GetBytes(out, "tools.2.type").String())
	assert.Equal(t, "apply_patch", gjson.GetBytes(out, "tools.2.name").String())
	assert.Equal(t, "function", gjson.GetBytes(out, "tools.3.type").String())
	assert.Equal(t, "terminal__exec", gjson.GetBytes(out, "tools.3.name").String())
	assert.Equal(t, "message", gjson.GetBytes(out, "input.0.type").String())
	assert.False(t, gjson.GetBytes(out, "input.0.phase").Exists())
	assert.Equal(t, "msg_message_commentary", gjson.GetBytes(out, "input.0.id").String())
	assert.Equal(t, "function_call", gjson.GetBytes(out, "input.1.type").String())
	assert.Equal(t, "fc_existing", gjson.GetBytes(out, "input.1.id").String())
	assert.Equal(t, "function_call_output", gjson.GetBytes(out, "input.2.type").String())
	assert.Equal(t, "function_call", gjson.GetBytes(out, "input.3.type").String())
	assert.Equal(t, "fc_search", gjson.GetBytes(out, "input.3.id").String())
	assert.True(t, strings.HasPrefix(gjson.GetBytes(out, "input.4.id").String(), "rs_"))
	assert.LessOrEqual(t, len([]rune(gjson.GetBytes(out, "input.4.id").String())), codexInputItemIDLimit)
	assert.Equal(t, "web_search_call", gjson.GetBytes(out, "input.5.type").String())
	assert.True(t, strings.HasPrefix(gjson.GetBytes(out, "input.5.id").String(), "ws_"))
	assert.LessOrEqual(t, len([]rune(gjson.GetBytes(out, "input.5.id").String())), codexInputItemIDLimit)
	assert.Equal(t, "function_call", gjson.GetBytes(out, "input.6.type").String())
	assert.Equal(t, "fc_provider", gjson.GetBytes(out, "input.6.id").String())
	assert.Equal(t, "audit__lookup", gjson.GetBytes(out, "input.6.name").String())
}

func TestXAIContractUsesNativeSearchAndDropsUnsupportedCodexTools(t *testing.T) {
	raw := []byte(`{
		"model":"grok-4.7",
		"tools":[
			{"type":"web_search","external_web_access":true},
			{"type":"tool_search"},
			{"type":"image_generation"},
			{"type":"custom","name":"apply_patch"},
			{"type":"custom","name":"terminal_exec"},
			{"type":"namespace","name":"mcp__demo","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}
		],
		"tool_choice":{"type":"web_search","external_web_access":true},
		"input":[
			{"type":"tool_search_call","id":"tsc_search","call_id":"call_search","arguments":{"query":"tools"}},
			{"type":"tool_search_output","call_id":"call_search","tools":[{"type":"namespace","name":"loaded","tools":[{"type":"function","name":"run","parameters":{"type":"object"}}]}]},
			{"type":"custom_tool_call","id":"ctc_custom","call_id":"call_custom","name":"terminal_exec","input":"pwd"},
			{"type":"custom_tool_call_output","id":"ctco_custom","call_id":"call_custom","output":"ok"}
		]
	}`)

	out, err := NormalizeOpenAIResponsesRequestForContract(raw, ProviderContractXAI)
	require.NoError(t, err)

	assert.Equal(t, "web_search", gjson.GetBytes(out, "tools.0.type").String())
	assert.False(t, gjson.GetBytes(out, "tools.0.external_web_access").Exists())
	assert.Equal(t, "function", gjson.GetBytes(out, "tools.1.type").String())
	assert.Equal(t, "terminal_exec", gjson.GetBytes(out, "tools.1.name").String())
	assert.Equal(t, "mcp__demo__lookup", gjson.GetBytes(out, "tools.2.name").String())
	assert.Equal(t, "loaded__run", gjson.GetBytes(out, "tools.3.name").String())
	assert.NotContains(t, string(out), `"type":"tool_search"`)
	assert.NotContains(t, string(out), `"type":"image_generation"`)
	assert.NotContains(t, string(out), `"name":"apply_patch"`)
	assert.NotContains(t, string(out), `"external_web_access"`)
	assert.Equal(t, "allowed_tools", gjson.GetBytes(out, "tool_choice.type").String())
	assert.Equal(t, "required", gjson.GetBytes(out, "tool_choice.mode").String())
	assert.Equal(t, "web_search", gjson.GetBytes(out, "tool_choice.tools.0.type").String())
	assert.Equal(t, int64(2), gjson.GetBytes(out, "input.#").Int())
	assert.Equal(t, "function_call", gjson.GetBytes(out, "input.0.type").String())
	assert.False(t, gjson.GetBytes(out, "input.0.id").Exists())
	assert.Equal(t, "function_call_output", gjson.GetBytes(out, "input.1.type").String())
}
