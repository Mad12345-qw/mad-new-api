package main

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestMadAPIPayloadMapsCockpitAliasesOnlyForCockpitRequests(t *testing.T) {
	request := pluginapi.ExecutorRequest{
		Payload: []byte(`{"model":"gpt-5.5","messages":[]}`),
		Headers: http.Header{cockpitHeader: []string{"1"}},
	}
	if got := gjson.GetBytes(madAPIPayload(request), "model").String(); got != "claude-fable-5" {
		t.Fatalf("cockpit model = %q, want claude-fable-5", got)
	}

	request.Headers = http.Header{}
	if got := gjson.GetBytes(madAPIPayload(request), "model").String(); got != "gpt-5.5" {
		t.Fatalf("native model = %q, want gpt-5.5", got)
	}
}

func TestMadAPIPayloadLeavesUnknownCockpitModelsUntouched(t *testing.T) {
	request := pluginapi.ExecutorRequest{
		Payload: []byte(`{"model":"gpt-5.6-experimental","messages":[]}`),
		Headers: http.Header{cockpitHeader: []string{"1"}},
	}
	if got := gjson.GetBytes(madAPIPayload(request), "model").String(); got != "gpt-5.6-experimental" {
		t.Fatalf("unknown cockpit model = %q", got)
	}
}
