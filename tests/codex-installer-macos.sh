#!/bin/sh
set -eu

installer_path=${1:?installer path is required}
installer_path=$(CDPATH= cd -- "$(dirname -- "$installer_path")" && pwd)/$(basename -- "$installer_path")
temporary_root=${RUNNER_TEMP:-/tmp}
home="$temporary_root/mad-codex-desktop-$$"
fail() { printf 'Assertion failed: %s\n' "$1" >&2; exit 1; }
hash_file() { shasum -a 256 "$1" | awk '{print $1}'; }
run_install() { CODEX_HOME=$1 MADAPI_KEY=$2 /bin/sh "$installer_path"; }

! grep -Fq 'CODEX_CLI_PATH' "$installer_path" || fail 'CLI probing remains.'
! grep -Fq 'madapi.key' "$installer_path" || fail 'Command authentication remains.'
! grep -Fq 'command -v codex' "$installer_path" || fail 'CLI discovery remains.'
trap 'rm -rf "$home"' EXIT
mkdir -p "$home/sessions"
config="$home/config.toml"; key_file="$home/madapi.key"; cache="$home/models_cache.json"; session="$home/sessions/sentinel.jsonl"
cat > "$config" <<'EOF'
model_provider = "custom"
model = "deepseek-v4-flash"
  'model_catalog_json' = "cc-switch-model-catalog.json"
disable_response_storage = true
[model_providers.custom]
name = "Existing Provider"
base_url = "https://old.invalid/v1"
[model_providers.madapi]
name = "Old MadAPI"
[plugins."github@openai-curated"]
enabled = true
EOF
printf '%s' 'keep-me' > "$key_file"; printf '{}' > "$cache"; printf 'session' > "$session"
config_hash=$(hash_file "$config"); session_hash=$(hash_file "$session")
run_install "$home" 'sk-macos-first-key'
grep -Fq 'model_provider = "custom"' "$config" || fail 'Provider identity changed.'
grep -Fq 'model = "deepseek-v4-flash"' "$config" || fail 'Default model changed.'
grep -Fq 'name = "Existing Provider"' "$config" || fail 'Provider name changed.'
grep -Fq 'experimental_bearer_token = "sk-macos-first-key"' "$config" || fail 'Bearer token missing.'
grep -Fq 'requires_openai_auth = true' "$config" || fail 'Desktop auth setting missing.'
grep -Fq 'disable_response_storage = true' "$config" || fail 'Unrelated setting changed.'
! grep -Eq "^[[:space:]]*(model_catalog_json|\"model_catalog_json\"|'model_catalog_json')[[:space:]]*=" "$config" || fail 'Static catalog remains.'
! grep -Fq '[model_providers.custom.auth]' "$config" || fail 'Command auth was added.'
! grep -Fq '[model_providers.madapi]' "$config" || fail 'Temporary provider remains.'
[ "$(cat "$key_file")" = keep-me ] || fail 'Existing key file changed.'
[ ! -e "$cache" ] || fail 'Stale cache remains.'
[ "$(hash_file "$session")" = "$session_hash" ] || fail 'Session changed.'
backup=$(find "$home" -maxdepth 1 -name 'config.toml.madapi-backup-*' -type f)
[ -n "$backup" ] && [ "$(hash_file "$backup")" = "$config_hash" ] || fail 'Backup is not exact.'
run_install "$home" 'sk-macos-second-key'
grep -Fq 'experimental_bearer_token = "sk-macos-second-key"' "$config" || fail 'Repeat install did not update token.'
[ "$(grep -c '^\[model_providers\.custom\]$' "$config")" -eq 1 ] || fail 'Duplicate provider created.'
fresh="$home/fresh"; run_install "$fresh" 'sk-macos-fresh-key'
grep -Fq 'model_provider = "custom"' "$fresh/config.toml" || fail 'Fresh identity is wrong.'
grep -Fq 'model = "gpt-5.6-sol"' "$fresh/config.toml" || fail 'Fresh default missing.'
[ ! -e "$fresh/madapi.key" ] || fail 'Fresh install created key file.'
printf '%s\n' 'macOS desktop Codex installer acceptance passed.'
