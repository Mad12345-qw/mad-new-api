package oairesponses

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestLowerCustomToolsForFunctionProvider(t *testing.T) {
	info := &convmeta.Values{}
	req := dto.OpenAIResponsesRequest{
		Model: "provider-model",
		Tools: []byte(`[{"type":"custom","name":"apply_patch","description":"Apply a patch"}]`),
		Input: []byte(`[{"type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"*** Begin Patch"},{"type":"custom_tool_call_output","call_id":"call_1","output":"Done"}]`),
	}

	got, err := LowerCustomToolsForFunctionProvider(req, info)
	require.NoError(t, err)
	assert.Equal(t, "function", gjson.GetBytes(got.Tools, "0.type").String())
	assert.Equal(t, "apply_patch", gjson.GetBytes(got.Tools, "0.name").String())
	assert.Equal(t, "string", gjson.GetBytes(got.Tools, "0.parameters.properties.input.type").String())
	assert.Equal(t, "input", gjson.GetBytes(got.Tools, "0.parameters.required.0").String())
	assert.False(t, gjson.GetBytes(got.Tools, "0.parameters.additionalProperties").Bool())
	assert.Equal(t, ResponsesInputTypeFunctionCall, gjson.GetBytes(got.Input, "0.type").String())
	assert.Equal(t, `{"input":"*** Begin Patch"}`, gjson.GetBytes(got.Input, "0.arguments").String())
	assert.Equal(t, ResponsesInputTypeFunctionCallOutput, gjson.GetBytes(got.Input, "1.type").String())
	assert.True(t, info.IsResponsesCustomTool("apply_patch"))
}
