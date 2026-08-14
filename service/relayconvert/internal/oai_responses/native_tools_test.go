package oairesponses

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
)

func nativeResponsesTestContext() *gin.Context {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	return context
}

func TestOpenAIResponsesToClaudePreservesNativeWebSearch(t *testing.T) {
	request := &dto.OpenAIResponsesRequest{
		Model:              "claude-test",
		Input:              json.RawMessage(`"search"`),
		PreviousResponseID: "resp_previous",
		Tools:              json.RawMessage(`[{"type":"web_search","search_context_size":"high","user_location":{"type":"approximate","country":"CN"}}]`),
	}

	out, err := OpenAIResponsesRequestToClaudeMessages(nativeResponsesTestContext(), request)
	if err != nil {
		t.Fatal(err)
	}
	tools, ok := out.Tools.([]any)
	if !ok {
		t.Fatalf("tools type = %T", out.Tools)
	}
	if len(tools) != 1 {
		t.Fatalf("tools length = %d", len(tools))
	}
	search, ok := tools[0].(*dto.ClaudeWebSearchTool)
	if !ok {
		t.Fatalf("tool type = %T", tools[0])
	}
	if search.Type != "web_search_20250305" || search.MaxUses != 10 {
		t.Fatalf("search tool = %#v", search)
	}
	if search.UserLocation == nil || search.UserLocation.Country != "CN" {
		t.Fatalf("user location = %#v", search.UserLocation)
	}
}

func TestOpenAIResponsesToGeminiPreservesNativeBuiltins(t *testing.T) {
	request := &dto.OpenAIResponsesRequest{
		Model:             "gemini-test",
		Input:             json.RawMessage(`"search"`),
		ContextManagement: json.RawMessage(`{"type":"compaction"}`),
		Tools: json.RawMessage(`[
			{"type":"web_search"},
			{"type":"code_interpreter"},
			{"type":"url_context"}
		]`),
	}

	out, err := OpenAIResponsesRequestToGeminiChat(nativeResponsesTestContext(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := out.GetTools()
	if len(tools) != 3 {
		t.Fatalf("tools length = %d", len(tools))
	}
	if tools[0].GoogleSearch == nil {
		t.Fatal("googleSearch was not preserved")
	}
	if tools[1].CodeExecution == nil {
		t.Fatal("codeExecution was not preserved")
	}
	if tools[2].URLContext == nil {
		t.Fatal("urlContext was not preserved")
	}
}
