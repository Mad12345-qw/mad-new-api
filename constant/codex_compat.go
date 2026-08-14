package constant

// CodexAPIModelSlots maps the eight stable API-login client shells to the
// NewAPI model IDs selected by the latest v4 installer contract.
var CodexAPIModelSlots = map[string]string{
	"gpt-5.5":       "claude-fable-5",
	"gpt-5.4":       "claude-opus-5",
	"gpt-5.6-sol":   "gpt-5.6-sol",
	"gpt-5.6-terra": "gpt-5.6-terra",
	"gpt-5.6-luna":  "gpt-5.6-luna",
	"gpt-5.4-mini":  "grok-4.6",
	"gpt-5.3-codex": "gpt-5.6-sol-pro",
	"gpt-5.2":       "gpt-5.6-terra-pro",
}
