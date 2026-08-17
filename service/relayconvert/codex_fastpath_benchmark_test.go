package relayconvert_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/gin-gonic/gin"
)

func BenchmarkCodexOpenAIResponsesConversion(b *testing.B) {
	inputText := strings.Repeat("context ", 131072)
	raw, err := common.Marshal(map[string]any{
		"model": "gpt-5.6-luna-high",
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": inputText},
				},
			},
		},
		"tools": []any{
			map[string]any{"type": "tool_search"},
			map[string]any{"type": "custom", "name": "apply_patch", "format": map[string]any{"type": "text"}},
		},
		"stream": true,
	})
	if err != nil {
		b.Fatal(err)
	}
	boundary, err := relayconvert.NormalizeCodexResponsesRequest(raw)
	if err != nil {
		b.Fatal(err)
	}
	var parsed dto.OpenAIResponsesRequest
	if err = common.Unmarshal(boundary, &parsed); err != nil {
		b.Fatal(err)
	}
	codexContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	codexContext.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	codexContext.Request.RemoteAddr = "127.0.0.1:12345"
	codexContext.Request.Header.Set("X-MadAPI-Codex-Compat", relayconvert.CodexResponsesInternalMarker())

	b.Run("r23-existing-pipeline", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			request, copyErr := common.DeepCopy(&parsed)
			if copyErr != nil {
				b.Fatal(copyErr)
			}
			request.Model = "gpt-5.6-luna-high"
			*request, err = relayconvert.NormalizeCodexResponsesRequestForSelectedProvider(*request, appconstant.APITypeOpenAI)
			if err != nil {
				b.Fatal(err)
			}
			converted, convertErr := (&openai.Adaptor{}).ConvertOpenAIResponsesRequest(codexContext, matrixInfo("gpt-5.6-luna-high"), *request)
			if convertErr != nil {
				b.Fatal(convertErr)
			}
			if _, err = common.Marshal(converted); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("r24-openai-fast-path", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, _, err = relayconvert.NormalizeCodexOpenAIResponsesRawRequest(boundary, "gpt-5.6-luna-high"); err != nil {
				b.Fatal(err)
			}
		}
	})
}
