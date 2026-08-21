package relayconvert

import "github.com/QuantumNous/new-api/dto"

import codexresponses "github.com/QuantumNous/new-api/service/relayconvert/internal/codex_responses"

const CodexResponsesRestoreInstallerContextKey = "madapi_codex_responses_restore_installer"

type CodexResponsesRestoreInstaller func(func([]byte) ([]byte, error))

func NormalizeCodexResponsesRequest(rawJSON []byte) ([]byte, error) {
	return codexresponses.NormalizeOpenAIResponsesRequest(rawJSON)
}

func RestoreCodexResponsesPayload(originalRequest, rawJSON []byte) ([]byte, error) {
	return codexresponses.RestoreOpenAIResponsesPayload(originalRequest, rawJSON)
}

// PrepareCodexResponsesRequest normalizes an already-decoded request in place
// and returns a lightweight per-event response restorer.
func PrepareCodexResponsesRequest(request *dto.OpenAIResponsesRequest) (func([]byte) ([]byte, error), error) {
	compat, err := codexresponses.PrepareOpenAIResponsesRequest(request)
	if err != nil {
		return nil, err
	}
	return compat.RestorePayload, nil
}

func CodexResponsesRequestNeedsNormalization(request *dto.OpenAIResponsesRequest) bool {
	return codexresponses.OpenAIResponsesRequestNeedsNormalization(request)
}
