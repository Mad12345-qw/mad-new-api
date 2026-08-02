#!/bin/sh
set -eu

diagnostic_path=${1:?diagnostic path is required}
diagnostic_path=$(CDPATH= cd -- "$(dirname -- "$diagnostic_path")" && pwd)/$(basename -- "$diagnostic_path")
temporary_root=${RUNNER_TEMP:-/tmp}
test_root="$temporary_root/mad-codex-diagnostic-$$"
test_home="$test_root/home"
codex_home="$test_home/.codex"
fake_codex="$test_root/codex"

fail() {
  printf 'Assertion failed: %s\n' "$1" >&2
  exit 1
}
hash_file() {
  shasum -a 256 "$1" | awk '{print $1}'
}
cleanup() {
  rm -rf "$test_root"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

mkdir -p "$codex_home/sessions" "$test_home/Desktop"
cat > "$codex_home/config.toml" <<'EOF'
model_provider = "custom"
[model_providers.custom]
base_url = "https://mad.invalid/codex/v1"
experimental_bearer_token = "sk-test-secret"
EOF
printf '%s' '{"auth_mode":"chatgpt"}' > "$codex_home/auth.json"
printf '%s' 'session-sentinel' > "$codex_home/sessions/sentinel.jsonl"
printf '%s' 'existing-cache-sentinel' > "$codex_home/models_cache.json"

cat > "$fake_codex" <<'EOF'
#!/bin/sh
set -eu
if [ "${1:-}" = '--version' ]; then
  printf '%s\n' 'codex-test 1.0.0'
  exit 0
fi
if [ "${1:-}" = 'app-server' ]; then
  cat >/dev/null
  printf '%s\n' '{"id":1,"result":{"userAgent":"codex-test"}}'
  printf '%s\n' '{"id":2,"result":{"data":[{"id":"deepseek-v4-flash"},{"id":"grok-4"}],"nextCursor":null}}'
  printf '%s' '{"models":[{"slug":"deepseek-v4-flash"},{"slug":"grok-4"}]}' > "$CODEX_HOME/models_cache.json"
  exit 0
fi
exit 2
EOF
chmod 700 "$fake_codex"

config_hash=$(hash_file "$codex_home/config.toml")
auth_hash=$(hash_file "$codex_home/auth.json")
session_hash=$(hash_file "$codex_home/sessions/sentinel.jsonl")
cache_hash=$(hash_file "$codex_home/models_cache.json")

HOME="$test_home" CODEX_HOME="$codex_home" CODEX_APP_SERVER_PATH="$fake_codex" /bin/sh "$diagnostic_path"

[ "$(hash_file "$codex_home/config.toml")" = "$config_hash" ] || fail 'Config changed.'
[ "$(hash_file "$codex_home/auth.json")" = "$auth_hash" ] || fail 'Auth state changed.'
[ "$(hash_file "$codex_home/sessions/sentinel.jsonl")" = "$session_hash" ] || fail 'Session changed.'
[ "$(hash_file "$codex_home/models_cache.json")" = "$cache_hash" ] || fail 'Existing model cache changed.'

report_dir=$(find "$test_home/Desktop" -maxdepth 1 -type d -name 'Codex-app-server-check-*' | head -n 1)
[ -n "$report_dir" ] || fail 'Report directory missing.'
[ -f "$report_dir.zip" ] || fail 'Report archive missing.'
grep -Fq 'experimental_bearer_token = "<redacted>"' "$report_dir/config-redacted.toml" || fail 'Bearer token was not redacted.'
! grep -Fq 'sk-test-secret' "$report_dir/config-redacted.toml" || fail 'Bearer token leaked.'
grep -Fq 'deepseek-v4-flash' "$report_dir/app-server-stdout.jsonl" || fail 'Live model/list output missing.'
grep -Fq 'grok-4' "$report_dir/models_cache.json" || fail 'Isolated model cache missing.'
grep -Fq 'Isolated app-server created models_cache.json.' "$report_dir/environment.txt" || fail 'Cache result missing.'

printf '%s\n' 'macOS desktop app-server diagnostic acceptance passed.'
