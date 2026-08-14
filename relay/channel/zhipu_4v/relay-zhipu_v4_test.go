package zhipu_4v

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRequestOpenAI2ZhipuBuildsNativeWebSearchTool(t *testing.T) {
	converted := requestOpenAI2Zhipu(dto.GeneralOpenAIRequest{
		Model:     "glm-5-2",
		WebSearch: json.RawMessage(`{"enable":true}`),
		Tools: []dto.ToolCallRequest{{
			Type: "function",
			Function: dto.FunctionRequest{
				Name:       "lookup",
				Parameters: map[string]any{"type": "object"},
			},
		}},
	})

	raw, err := json.Marshal(converted)
	require.NoError(t, err)
	require.Equal(t, int64(2), gjson.GetBytes(raw, "tools.#").Int())
	require.Equal(t, "lookup", gjson.GetBytes(raw, "tools.0.function.name").String())
	require.Equal(t, "web_search", gjson.GetBytes(raw, "tools.1.type").String())
	require.True(t, gjson.GetBytes(raw, "tools.1.web_search.enable").Bool())
	require.False(t, gjson.GetBytes(raw, "tools.1.function").Exists())
}
