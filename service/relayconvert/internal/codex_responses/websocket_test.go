package codexresponses

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWebsocketStateRejectsAppendBeforeCreate(t *testing.T) {
	state := &WebsocketState{}
	_, err := state.NormalizeWebsocketRequest([]byte(`{"type":"response.append","input":[]}`), 1<<20)
	require.ErrorContains(t, err, "before response.create")
}

func TestWebsocketStateMergesHTTPFallbackTranscript(t *testing.T) {
	state := &WebsocketState{}
	first, err := state.NormalizeWebsocketRequest([]byte(`{
		"type":"response.create",
		"model":"gpt-test",
		"instructions":"be concise",
		"input":[{"type":"message","role":"user","id":"user-1","content":"hello"}]
	}`), 1<<20)
	require.NoError(t, err)
	require.NotContains(t, string(first), `"type":"response.create"`)
	require.Contains(t, string(first), `"stream":true`)

	completed := []byte(`{
		"type":"response.completed",
		"response":{"id":"resp-1","output":[{"type":"message","role":"assistant","id":"assistant-1","content":"hi"}]}
	}`)
	require.True(t, state.ObserveWebsocketEvent(completed, make(map[int]json.RawMessage), &[]json.RawMessage{}))

	second, err := state.NormalizeWebsocketRequest([]byte(`{
		"type":"response.append",
		"previous_response_id":"resp-1",
		"input":[{"type":"message","role":"user","id":"user-2","content":"next"}]
	}`), 1<<20)
	require.NoError(t, err)
	var request struct {
		Model        string            `json:"model"`
		Instructions string            `json:"instructions"`
		Input        []json.RawMessage `json:"input"`
	}
	require.NoError(t, json.Unmarshal(second, &request))
	require.Equal(t, "gpt-test", request.Model)
	require.Equal(t, "be concise", request.Instructions)
	require.Len(t, request.Input, 3)
	require.Contains(t, string(request.Input[0]), `"id":"user-1"`)
	require.Contains(t, string(request.Input[1]), `"id":"assistant-1"`)
	require.Contains(t, string(request.Input[2]), `"id":"user-2"`)
	require.NotContains(t, string(second), "previous_response_id")
}

func TestWebsocketStateReplacesExplicitTranscript(t *testing.T) {
	state := &WebsocketState{}
	_, err := state.NormalizeWebsocketRequest([]byte(`{"type":"response.create","model":"gpt-test","input":[{"type":"message","role":"user","id":"old"}]}`), 1<<20)
	require.NoError(t, err)

	next, err := state.NormalizeWebsocketRequest([]byte(`{"type":"response.append","input":[{"type":"message","role":"assistant","id":"replacement"}]}`), 1<<20)
	require.NoError(t, err)
	require.Contains(t, string(next), `"id":"replacement"`)
	require.NotContains(t, string(next), `"id":"old"`)
}

func TestWebsocketStateEnforcesMemoryLimit(t *testing.T) {
	state := &WebsocketState{}
	_, err := state.NormalizeWebsocketRequest([]byte(`{"type":"response.create","model":"gpt-test","input":[{"type":"message","role":"user","content":"payload"}]}`), 32)
	require.ErrorContains(t, err, "exceeds")
}

func TestWebsocketStateSnapshotRestoresFailedTurn(t *testing.T) {
	state := &WebsocketState{}
	snapshot := state.Snapshot()
	_, err := state.NormalizeWebsocketRequest([]byte(`{"type":"response.create","model":"gpt-test","input":[{"type":"message","role":"user","id":"failed"}]}`), 1<<20)
	require.NoError(t, err)
	require.True(t, state.HasRequest())

	state.Restore(snapshot)
	require.False(t, state.HasRequest())
	next, err := state.NormalizeWebsocketRequest([]byte(`{"type":"response.create","model":"gpt-test","input":[{"type":"message","role":"user","id":"retry"}]}`), 1<<20)
	require.NoError(t, err)
	require.Contains(t, string(next), `"id":"retry"`)
	require.NotContains(t, string(next), `"id":"failed"`)
}
