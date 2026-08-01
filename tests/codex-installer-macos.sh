#!/bin/sh
set -eu

installer_path=${1:?installer path is required}
sandbox="$RUNNER_TEMP/mad-codex-macos-$$"
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
catalog_fixture="$script_dir/fixtures/codex-models.json"

fail() {
  printf 'Assertion failed: %s\n' "$1" >&2
  exit 1
}

hash_file() {
  shasum -a 256 "$1" | awk '{print $1}'
}

run_installer() {
  home=$1
  key=$2
  cli=$3
  CODEX_HOME="$home" MADAPI_KEY="$key" CODEX_CLI_PATH="$cli" /bin/sh "$installer_path"
}

cleanup() {
  rm -rf "$sandbox"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

mkdir -p "$sandbox"
codex_cli=$(command -v codex)
[ -x "$codex_cli" ] || fail 'Latest Codex CLI is unavailable.'

home="$sandbox/complex"
session_dir="$home/sessions/2026/08/01"
mkdir -p "$session_dir"
config_path="$home/config.toml"
session_path="$session_dir/sentinel.jsonl"
cat > "$config_path" <<EOF
model_provider = "custom"
model = "old-model"
model_catalog_json = "$catalog_fixture"
model_reasoning_effort = "medium"

[features]
memories = true

[model_providers.custom]
name = "Existing Provider"
base_url = "https://example.invalid/v1"
wire_api = "responses"

[model_providers.madapi]
name = "Old MadAPI"
base_url = "https://old.example.invalid/v1"
wire_api = "responses"
experimental_bearer_token = "old-secret"

[projects.'D:\桌面\项目']
trust_level = "trusted"

[plugins."github@openai-curated"]
enabled = true

[mcp_servers.node_repl]
command = '/usr/local/bin/node_repl'
args = []
EOF
printf '%s' '{"type":"sentinel"}' > "$session_path"
config_hash=$(hash_file "$config_path")
session_hash=$(hash_file "$session_path")

run_installer "$home" 'sk-macos-first-key' "$codex_cli"
grep -Fq "[projects.'D:\桌面\项目']" "$config_path" || fail 'UTF-8 project path was not preserved.'
grep -Fq '[model_providers.custom]' "$config_path" || fail 'Existing provider was not preserved.'
grep -Fq 'model_provider = "custom"' "$config_path" || fail 'Existing provider identity was changed.'
grep -Fq 'name = "Existing Provider"' "$config_path" || fail 'Existing provider display name was changed.'
! grep -Fq '[model_providers.madapi]' "$config_path" || fail 'A second provider identity was created.'
grep -Fq '[plugins."github@openai-curated"]' "$config_path" || fail 'Plugin configuration was not preserved.'
grep -Fq '[mcp_servers.node_repl]' "$config_path" || fail 'MCP configuration was not preserved.'
grep -Fq 'model_reasoning_effort = "medium"' "$config_path" || fail 'Existing reasoning effort was overwritten.'
! grep -Fq 'old-secret' "$config_path" || fail 'Old MadAPI secret remained in config.toml.'
grep -Fq 'experimental_bearer_token = "sk-macos-first-key"' "$config_path" || fail 'API key was not written into the active provider.'
[ "$(grep -c '^experimental_bearer_token = "sk-macos-first-key"$' "$config_path")" -eq 1 ] || fail 'API key was written more than once.'
! grep -Fq 'madapi.key' "$config_path" || fail 'Obsolete key-file authentication remained configured.'
! grep -Fq 'model_catalog_json' "$config_path" || fail 'A static model catalog remained configured.'
! grep -Fq '[model_providers.custom.auth]' "$config_path" || fail 'Command-backed authentication remained configured.'
grep -Fq 'supports_websockets = true' "$config_path" || fail 'MadAPI WebSocket support was not enabled.'
grep -Fq 'requires_openai_auth = true' "$config_path" || fail 'Remote model catalog authentication was not enabled.'
[ "$(hash_file "$session_path")" = "$session_hash" ] || fail 'Session data changed.'

backup=$(find "$home" -maxdepth 1 -name 'config.toml.madapi-backup-*' -type f)
[ -n "$backup" ] || fail 'Config backup was not created.'
[ "$(hash_file "$backup")" = "$config_hash" ] || fail 'Backup is not byte-identical to the original config.'
[ "$(stat -f '%Lp' "$config_path")" = '600' ] || fail 'Config containing the API key permissions are not 600.'
CODEX_HOME="$home" "$codex_cli" features list >/dev/null

sleep 1
run_installer "$home" 'sk-macos-second-key' "$codex_cli"
[ "$(grep -c '^\[model_providers\.custom\]$' "$config_path")" -eq 1 ] || fail 'Duplicate provider section was created.'
[ "$(grep -c '^experimental_bearer_token = "sk-macos-second-key"$' "$config_path")" -eq 1 ] || fail 'Repeat install did not replace the API key exactly once.'
! grep -Fq 'sk-macos-first-key' "$config_path" || fail 'Repeat install retained the previous API key.'
[ "$(grep -c '^supports_websockets = true$' "$config_path")" -eq 1 ] || fail 'Duplicate WebSocket setting was created.'

recovery_home="$sandbox/recover-provider"
mkdir -p "$recovery_home"
cat > "$recovery_home/config.toml" <<'EOF'
model_provider = "madapi"
model = "gpt-5.6-sol"

[model_providers.custom]
name = "custom"
base_url = "https://mad.myddns.me/codex/v1"
wire_api = "responses"

[model_providers.madapi]
name = "MadAPI"
base_url = "https://mad.myddns.me/codex/v1"
wire_api = "responses"
EOF
cat > "$recovery_home/config.toml.madapi-backup-20260801-010101-001" <<'EOF'
model_provider = "custom"
model = "deepseek-v4-flash"

[model_providers.custom]
name = "custom"
base_url = "https://mad.myddns.me/codex/v1"
wire_api = "responses"
requires_openai_auth = true
EOF
run_installer "$recovery_home" 'sk-macos-recovery-key' "$codex_cli"
grep -Fq 'model_provider = "custom"' "$recovery_home/config.toml" || fail 'Original provider identity was not recovered from backup.'
! grep -Fq '[model_providers.madapi]' "$recovery_home/config.toml" || fail 'Temporary MadAPI provider identity remained after recovery.'

fresh_home="$sandbox/fresh"
run_installer "$fresh_home" 'sk-macos-fresh-key' "$codex_cli"
grep -Fq 'model_provider = "custom"' "$fresh_home/config.toml" || fail 'Fresh install did not use the proven custom provider identity.'
grep -Fq 'name = "custom"' "$fresh_home/config.toml" || fail 'Fresh install did not use the proven custom provider display name.'
grep -Fq 'experimental_bearer_token = "sk-macos-fresh-key"' "$fresh_home/config.toml" || fail 'Fresh install did not configure the MadAPI key.'
CODEX_HOME="$fresh_home" "$codex_cli" features list >/dev/null

official_home="$sandbox/official-provider"
mkdir -p "$official_home"
cat > "$official_home/config.toml" <<'EOF'
model_provider = "openai"
model = "gpt-5.6-sol"

[features]
memories = true
EOF
run_installer "$official_home" 'sk-macos-official-key' "$codex_cli"
grep -Fq 'model_provider = "custom"' "$official_home/config.toml" || fail 'Reserved OpenAI provider was not moved to the proven custom provider.'
grep -Fq '[model_providers.custom]' "$official_home/config.toml" || fail 'Custom MadAPI provider was not created for the reserved OpenAI provider.'
! grep -Fq '[model_providers.openai]' "$official_home/config.toml" || fail 'Reserved OpenAI provider was illegally overridden.'
grep -Fq '[features]' "$official_home/config.toml" || fail 'Existing official-provider configuration was not preserved.'

bad_home="$sandbox/malformed"
mkdir -p "$bad_home"
printf 'broken = [\n' > "$bad_home/config.toml"
bad_hash=$(hash_file "$bad_home/config.toml")
if run_installer "$bad_home" 'sk-macos-bad-key' "$codex_cli" >/dev/null 2>&1; then
  fail 'Malformed existing config was accepted.'
fi
[ "$(hash_file "$bad_home/config.toml")" = "$bad_hash" ] || fail 'Malformed existing config was changed.'

rollback_home="$sandbox/rollback"
mkdir -p "$rollback_home"
printf 'model = "old-model"\n' > "$rollback_home/config.toml"
printf '%s' 'sk-old-key' > "$rollback_home/madapi.key"
printf '%s' '{"models":[{"slug":"old-model"}]}' > "$rollback_home/madapi-models.json"
rollback_hash=$(hash_file "$rollback_home/config.toml")
rollback_catalog_hash=$(hash_file "$rollback_home/madapi-models.json")
fake_cli="$sandbox/fake-codex"
counter="$sandbox/fake-count"
cat > "$fake_cli" <<'EOF'
#!/bin/sh
set -eu
if [ "${1:-}" = '--version' ]; then
  exit 0
fi
count=0
[ ! -f "$CODEX_FAKE_COUNT" ] || count=$(cat "$CODEX_FAKE_COUNT")
count=$((count + 1))
printf '%s' "$count" > "$CODEX_FAKE_COUNT"
[ "$count" -lt 2 ]
EOF
chmod 700 "$fake_cli"
if CODEX_FAKE_COUNT="$counter" run_installer "$rollback_home" 'sk-new-key' "$fake_cli" >/dev/null 2>&1; then
  fail 'Forced post-write validation failure was accepted.'
fi
[ "$(hash_file "$rollback_home/config.toml")" = "$rollback_hash" ] || fail 'Config was not rolled back byte-for-byte.'
[ "$(cat "$rollback_home/madapi.key")" = 'sk-old-key' ] || fail 'Unrelated legacy key file was changed.'
[ "$(hash_file "$rollback_home/madapi-models.json")" = "$rollback_catalog_hash" ] || fail 'Unrelated legacy model catalog was changed.'

printf '%s\n' 'macOS Codex installer acceptance passed.'
