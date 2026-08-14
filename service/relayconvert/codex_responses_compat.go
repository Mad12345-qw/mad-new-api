package relayconvert

import codexresponses "github.com/QuantumNous/new-api/service/relayconvert/internal/codex_responses"

type CodexResponsesWebsocketState = codexresponses.WebsocketState

func NormalizeCodexResponsesRequest(rawJSON []byte) ([]byte, error) {
	return codexresponses.NormalizeOpenAIResponsesRequest(rawJSON)
}

func RestoreCodexResponsesPayload(originalRequest, rawJSON []byte) ([]byte, error) {
	return codexresponses.RestoreOpenAIResponsesPayload(originalRequest, rawJSON)
}
