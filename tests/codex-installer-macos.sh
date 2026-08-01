#!/bin/sh
set -eu

installer_path=${1:?installer path is required}
sandbox="$RUNNER_TEMP/mad-codex-macos-$$"

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
cat > "$config_path" <<'EOF'
model_provider = "custom"
model = "old-model"
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
grep -Fq '[plugins."github@openai-curated"]' "$config_path" || fail 'Plugin configuration was not preserved.'
grep -Fq '[mcp_servers.node_repl]' "$config_path" || fail 'MCP configuration was not preserved.'
grep -Fq 'model_reasoning_effort = "medium"' "$config_path" || fail 'Existing reasoning effort was overwritten.'
! grep -Fq 'old-secret' "$config_path" || fail 'Old MadAPI secret remained in config.toml.'
! grep -Fq 'sk-macos-first-key' "$config_path" || fail 'API key was written into config.toml.'
grep -Fq 'madapi.key' "$config_path" || fail 'Protected key-file auth was not configured.'
grep -Fq 'supports_websockets = true' "$config_path" || fail 'MadAPI WebSocket support was not enabled.'
[ "$(hash_file "$session_path")" = "$session_hash" ] || fail 'Session data changed.'

backup=$(find "$home" -maxdepth 1 -name 'config.toml.madapi-backup-*' -type f)
[ -n "$backup" ] || fail 'Config backup was not created.'
[ "$(hash_file "$backup")" = "$config_hash" ] || fail 'Backup is not byte-identical to the original config.'
[ "$(cat "$home/madapi.key")" = 'sk-macos-first-key' ] || fail 'Protected key file has the wrong value.'
[ "$(stat -f '%Lp' "$home/madapi.key")" = '600' ] || fail 'Key file permissions are not 600.'
CODEX_HOME="$home" "$codex_cli" features list >/dev/null

sleep 1
run_installer "$home" 'sk-macos-second-key' "$codex_cli"
[ "$(grep -c '^\[model_providers\.madapi\]$' "$config_path")" -eq 1 ] || fail 'Duplicate provider section was created.'
[ "$(grep -c '^\[model_providers\.madapi\.auth\]$' "$config_path")" -eq 1 ] || fail 'Duplicate auth section was created.'
[ "$(grep -c '^supports_websockets = true$' "$config_path")" -eq 1 ] || fail 'Duplicate WebSocket setting was created.'
[ "$(cat "$home/madapi.key")" = 'sk-macos-second-key' ] || fail 'Repeat install did not rotate the key.'

fresh_home="$sandbox/fresh"
run_installer "$fresh_home" 'sk-macos-fresh-key' "$codex_cli"
CODEX_HOME="$fresh_home" "$codex_cli" features list >/dev/null

bad_home="$sandbox/malformed"
mkdir -p "$bad_home"
printf 'broken = [\n' > "$bad_home/config.toml"
bad_hash=$(hash_file "$bad_home/config.toml")
if run_installer "$bad_home" 'sk-macos-bad-key' "$codex_cli" >/dev/null 2>&1; then
  fail 'Malformed existing config was accepted.'
fi
[ "$(hash_file "$bad_home/config.toml")" = "$bad_hash" ] || fail 'Malformed existing config was changed.'
[ ! -f "$bad_home/madapi.key" ] || fail 'Key file was created for a rejected config.'

rollback_home="$sandbox/rollback"
mkdir -p "$rollback_home"
printf 'model = "old-model"\n' > "$rollback_home/config.toml"
printf '%s' 'sk-old-key' > "$rollback_home/madapi.key"
rollback_hash=$(hash_file "$rollback_home/config.toml")
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
[ "$(cat "$rollback_home/madapi.key")" = 'sk-old-key' ] || fail 'Previous key was not restored.'

printf '%s\n' 'macOS Codex installer acceptance passed.'
