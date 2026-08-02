#!/bin/sh
set -eu

installer_path=${1:?installer path is required}
installer_path=$(CDPATH= cd -- "$(dirname -- "$installer_path")" && pwd)/$(basename -- "$installer_path")
refresh_script_path=$(dirname -- "$installer_path")/refresh-model-catalog.sh
temporary_root=${RUNNER_TEMP:-/tmp}
home="$temporary_root/mad-codex-desktop-$$"
fail() { printf 'Assertion failed: %s\n' "$1" >&2; exit 1; }
hash_file() { shasum -a 256 "$1" | awk '{print $1}'; }
write_oauth() { printf '%s' '{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"access_token":"oauth-access-token","refresh_token":"oauth-refresh-token","id_token":"oauth-id-token"},"last_refresh":"2026-08-02T00:00:00Z"}' > "$1"; }
run_install() { CODEX_HOME=$1 MADAPI_KEY=$2 MADAPI_INSTALL_TEST_MODE=1 MADAPI_REFRESH_SCRIPT_SOURCE="$refresh_script_path" /bin/sh "$installer_path"; }

! grep -Fq 'CODEX_CLI_PATH' "$installer_path" || fail 'CLI probing remains.'
! grep -Fq 'madapi.key' "$installer_path" || fail 'Command authentication remains.'
! grep -Fq 'command -v codex' "$installer_path" || fail 'CLI discovery remains.'
trap 'rm -rf "$home"' EXIT
mkdir -p "$home/sessions"
config="$home/config.toml"; auth="$home/auth.json"; key_file="$home/madapi.key"; cache="$home/models_cache.json"; session="$home/sessions/sentinel.jsonl"
cat > "$config" <<'EOF'
model_provider = "custom"
model = "deepseek-v4-flash"
  'model_catalog_json' = "cc-switch-model-catalog.json"
disable_response_storage = true
[model_providers.custom]
name = "Existing Provider"
base_url = "https://old.invalid/v1"
requires_openai_auth = false
experimental_bearer_token = "sk-stale-bearer"
[model_providers.custom.auth]
command = "/bin/sh"
args = ["-c", "printf stale"]
[model_providers.madapi]
name = "Old MadAPI"
[plugins."github@openai-curated"]
enabled = true
EOF
write_oauth "$auth"; printf '%s' 'keep-me' > "$key_file"; printf '{}' > "$cache"; printf 'session' > "$session"
config_hash=$(hash_file "$config"); auth_hash=$(hash_file "$auth"); session_hash=$(hash_file "$session")
run_install "$home" 'sk-macos-first-key'
grep -Fq 'model_provider = "custom"' "$config" || fail 'Provider identity changed.'
grep -Fq 'model = "deepseek-v4-flash"' "$config" || fail 'Default model changed.'
grep -Fq 'name = "Existing Provider"' "$config" || fail 'Provider name changed.'
grep -Fq 'experimental_bearer_token = "sk-macos-first-key"' "$config" || fail 'Bearer token missing.'
grep -Fq 'requires_openai_auth = true' "$config" || fail 'Desktop auth setting missing.'
grep -Fq 'base_url = "https://mad.myddns.me/codex/cockpit/v1"' "$config" || fail 'OAuth Cockpit route is missing.'
grep -Fq 'disable_response_storage = true' "$config" || fail 'Unrelated setting changed.'
grep -Fq 'model_catalog_json = "madapi-cockpit-model-catalog.json"' "$config" || fail 'OAuth managed catalog is missing.'
! grep -Fq 'cc-switch-model-catalog.json' "$config" || fail 'Conflicting third-party catalog remains.'
! grep -Fq '[model_providers.custom.auth]' "$config" || fail 'Command auth was added.'
! grep -Fq 'sk-stale-bearer' "$config" || fail 'Stale bearer remains.'
! grep -Fq 'printf stale' "$config" || fail 'Stale command auth remains.'
! grep -Fq '[model_providers.madapi]' "$config" || fail 'Temporary provider remains.'
[ "$(cat "$key_file")" = keep-me ] || fail 'Existing key file changed.'
[ ! -e "$cache" ] || fail 'Stale cache remains.'
[ "$(hash_file "$auth")" = "$auth_hash" ] || fail 'OAuth state changed.'
[ "$(hash_file "$session")" = "$session_hash" ] || fail 'Session changed.'
[ -f "$home/madapi-refresh-model-catalog.sh" ] || fail 'OAuth refresh script is missing.'
[ -f "$home/madapi-cockpit-model-catalog.json" ] || fail 'OAuth catalog file is missing.'
backup=$(find "$home" -maxdepth 1 -name 'config.toml.madapi-backup-*' -type f)
[ -n "$backup" ] && [ "$(hash_file "$backup")" = "$config_hash" ] || fail 'Backup is not exact.'
run_install "$home" 'sk-macos-second-key'
grep -Fq 'experimental_bearer_token = "sk-macos-second-key"' "$config" || fail 'Repeat install did not update token.'
[ "$(grep -c '^\[model_providers\.custom\]$' "$config")" -eq 1 ] || fail 'Duplicate provider created.'
fresh="$home/fresh"; mkdir -p "$fresh"; write_oauth "$fresh/auth.json"; run_install "$fresh" 'sk-macos-fresh-key'
grep -Fq 'model_provider = "custom"' "$fresh/config.toml" || fail 'Fresh identity is wrong.'
grep -Fq 'model = "gpt-5.6-sol"' "$fresh/config.toml" || fail 'Fresh default missing.'
grep -Fq 'requires_openai_auth = true' "$fresh/config.toml" || fail 'Fresh OAuth setting missing.'
grep -Fq 'base_url = "https://mad.myddns.me/codex/cockpit/v1"' "$fresh/config.toml" || fail 'Fresh OAuth Cockpit route is missing.'
grep -Fq 'model_catalog_json = "madapi-cockpit-model-catalog.json"' "$fresh/config.toml" || fail 'Fresh OAuth managed catalog is missing.'
grep -Fq 'experimental_bearer_token = "sk-macos-fresh-key"' "$fresh/config.toml" || fail 'Fresh bearer token missing.'
[ ! -e "$fresh/madapi.key" ] || fail 'Fresh install created key file.'

api_only="$home/api-only"; mkdir -p "$api_only"
printf '%s' 'model = "gpt-5.6-sol"' > "$api_only/config.toml"
printf '%s' '{"OPENAI_API_KEY":"sk-existing-api-key","tokens":null,"last_refresh":null}' > "$api_only/auth.json"
printf '{}' > "$api_only/models_cache.json"
api_config_hash=$(hash_file "$api_only/config.toml"); api_auth_hash=$(hash_file "$api_only/auth.json")
run_install "$api_only" 'sk-macos-api-key'
grep -Fq 'requires_openai_auth = false' "$api_only/config.toml" || fail 'API-key auth gate is wrong.'
grep -Fq '[model_providers.custom.auth]' "$api_only/config.toml" || fail 'API-key command auth is missing.'
grep -Fq "printf %s 'sk-macos-api-key'" "$api_only/config.toml" || fail 'API-key command does not contain the MadAPI key.'
! grep -Fq 'experimental_bearer_token' "$api_only/config.toml" || fail 'API-key config contains conflicting bearer auth.'
grep -Fq 'model_catalog_json = "madapi-cockpit-model-catalog.json"' "$api_only/config.toml" || fail 'API-key managed catalog is missing.'
grep -Fq 'base_url = "https://mad.myddns.me/codex/cockpit/v1"' "$api_only/config.toml" || fail 'API-key Cockpit route is missing.'
! grep -Fq 'cc-switch-model-catalog.json' "$api_only/config.toml" || fail 'Conflicting third-party catalog remains.'
[ -f "$api_only/madapi-refresh-model-catalog.sh" ] || fail 'API-key refresh script is missing.'
[ -f "$api_only/madapi-cockpit-model-catalog.json" ] || fail 'API-key catalog file is missing.'
[ ! -e "$api_only/models_cache.json" ] || fail 'API-key stale cache remains.'
[ "$(/usr/bin/plutil -extract OPENAI_API_KEY raw -o - "$api_only/auth.json")" = sk-existing-api-key ] || fail 'Existing API-key authentication changed.'
[ "$(hash_file "$api_only/auth.json")" = "$api_auth_hash" ] || fail 'Existing API-key authentication was not preserved byte-for-byte.'
api_config_backup=$(find "$api_only" -maxdepth 1 -name 'config.toml.madapi-backup-*' -type f)
[ "$(hash_file "$api_config_backup")" = "$api_config_hash" ] || fail 'API-key config backup is not exact.'
[ -z "$(find "$api_only" -maxdepth 1 -name 'auth.json.madapi-backup-*' -type f)" ] || fail 'Installer created an unnecessary authentication backup.'

unsigned="$home/unsigned"; mkdir -p "$unsigned"
printf '%s' 'model = "gpt-5.6-sol"' > "$unsigned/config.toml"
run_install "$unsigned" 'sk-macos-new-key'
! grep -Fq '[model_providers.custom.auth]' "$unsigned/config.toml" || fail 'New-user install forced command authentication.'
grep -Fq 'requires_openai_auth = true' "$unsigned/config.toml" || fail 'New-user install did not preserve the Codex sign-in chooser.'
grep -Fq 'experimental_bearer_token = "sk-macos-new-key"' "$unsigned/config.toml" || fail 'New-user MadAPI bearer token is missing.'
grep -Fq 'model_catalog_json = "madapi-cockpit-model-catalog.json"' "$unsigned/config.toml" || fail 'New-user install did not configure the managed catalog.'
grep -Fq 'base_url = "https://mad.myddns.me/codex/cockpit/v1"' "$unsigned/config.toml" || fail 'New-user install did not configure the Cockpit route.'
[ ! -e "$unsigned/auth.json" ] || fail 'New-user install forced API-key sign-in.'
printf '%s\n' 'macOS desktop Codex installer acceptance passed.'
