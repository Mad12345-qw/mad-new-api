package main

import (
	"encoding/base64"
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

func TestIsValidSSEDataLine(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{line: "data: {\"choices\":[]}", want: true},
		{line: "data: [DONE]", want: false},
		{line: ": keep-alive", want: false},
		{line: "", want: false},
	}
	for _, test := range cases {
		if got := isValidSSEDataLine([]byte(test.line)); got != test.want {
			t.Fatalf("isValidSSEDataLine(%q) = %t, want %t", test.line, got, test.want)
		}
	}
}

func TestPluginConfigAcceptsBoundedBootstrapRetries(t *testing.T) {
	configYAML := "enabled: true\nbase_url: http://madapi/v1\nbootstrap_retries: 2\n"
	raw := []byte(`{"config_yaml":"` + base64.StdEncoding.EncodeToString([]byte(configYAML)) + `"}`)
	if err := configure(raw); err != nil {
		t.Fatalf("configure() error = %v", err)
	}
	if got := loadedConfig().BootstrapRetries; got != 2 {
		t.Fatalf("bootstrap retries = %d, want 2", got)
	}
}
