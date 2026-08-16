#!/bin/sh
set -eu

installer_path=${1:?installer path is required}
asset_root=$(CDPATH= cd -- "$(dirname -- "$installer_path")" && pwd)
repo_root=$(CDPATH= cd -- "$asset_root/../../../.." && pwd)
refresh_path=$asset_root/refresh-model-catalog.sh
history_path=$asset_root/restore-history.sh
oauth_models=$repo_root/tests/fixtures/oauth-codex-models.json
api_models=$repo_root/tests/fixtures/newapi-models.json
templates=$repo_root/tests/fixtures/codex-model-templates.json

fail() { printf '%s\n' "$1" >&2; exit 1; }

/bin/sh -n "$installer_path"
/bin/sh -n "$refresh_path"
/bin/sh -n "$history_path"
grep -Fq 'requires_openai_auth = true' "$installer_path" || fail 'OAuth gateway authentication branch is missing.'
grep -Fq 'experimental_bearer_token' "$installer_path" || fail 'OAuth bearer configuration is missing.'
grep -Fq 'env_key = ' "$installer_path" || fail 'Official custom gateway env_key is missing.'
grep -Fq 'x-openai-actor-authorization' "$installer_path" || fail 'Official gateway actor header is missing.'
grep -Fq 'supports_websockets = false' "$installer_path" || fail 'WebSocket opt-out is missing.'
grep -Fq 'image_generation = true' "$installer_path" || fail 'Codex image generation feature is missing.'
grep -Fq '/codex/v1' "$installer_path" || fail 'Dedicated Codex route is missing.'
grep -Fq '/codex/cockpit/v1' "$installer_path" || fail 'API compatibility route is missing.'
grep -Fq 'auth_mutation=clear' "$installer_path" || fail 'API-to-OAuth authentication cleanup is missing.'
grep -Fq 'if [ "$auth_mutation" != none ]' "$installer_path" || fail 'Authentication mutation transaction is incomplete.'
grep -Fq '/mad-codex/codex-model-templates.json' "$refresh_path" || fail 'Self-hosted Codex model template is missing.'
! grep -Fq 'models.router-for.me' "$refresh_path" || fail 'Codex model refresh still depends on a third-party host.'

if [ "$(uname -s)" != Darwin ]; then
  printf '%s\n' 'Codex macOS source contract passed; runtime acceptance requires macOS.'
  exit 0
fi

root=$(mktemp -d "${TMPDIR:-/tmp}/madapi-clean-codex.XXXXXX")
trap 'rm -rf "$root"' EXIT HUP INT TERM
codex_home=$root/.codex
mkdir -p "$codex_home/sessions"
config=$codex_home/config.toml
auth=$codex_home/auth.json
session=$codex_home/sessions/sentinel.jsonl
cat > "$config" <<'EOF'
model_provider = "custom"
model = "gpt-5.6-terra"
disable_response_storage = true

[model_providers.custom]
name = "custom"
base_url = "https://old.invalid/v1"

[features]
memories = true
EOF
printf '%s' '{"auth_mode":"chatgpt","tokens":{"access_token":"oauth-access","refresh_token":"oauth-refresh"}}' > "$auth"
printf '%s' 'session-sentinel' > "$session"
auth_hash=$(shasum -a 256 "$auth" | awk '{print $1}')
session_hash=$(shasum -a 256 "$session" | awk '{print $1}')

CODEX_HOME="$codex_home" \
MADAPI_KEY='sk-clean-macos-test' \
MADAPI_BASE_URL='http://127.0.0.1:13016' \
MADAPI_CODEX_LOGIN_MODE='oauth' \
MADAPI_INSTALL_TEST_MODE=1 \
MADAPI_REFRESH_SCRIPT_SOURCE="$refresh_path" \
MADAPI_HISTORY_RESTORE_SCRIPT_SOURCE="$history_path" \
MADAPI_IMAGE_SKILL_SOURCE_DIR="$asset_root/image-skill" \
MADAPI_REFRESH_RESPONSE_FILE="$oauth_models" \
MADAPI_CODEX_TEMPLATE_FILE="$templates" \
/bin/sh "$installer_path"

grep -Fq 'base_url = "http://127.0.0.1:13016/codex/v1"' "$config" || fail 'Dedicated Codex route is missing.'
grep -Fq 'requires_openai_auth = true' "$config" || fail 'OAuth gateway auth mode changed.'
grep -Fq 'experimental_bearer_token = ' "$config" || fail 'OAuth bearer token is missing.'
! grep -Fq 'env_key = "MADAPI_API_KEY"' "$config" || fail 'OAuth config incorrectly uses API env_key.'
grep -Fq 'image_generation = true' "$config" || fail 'Image generation was not enabled.'
grep -Fq 'localeOverride = "zh-CN"' "$config" || fail 'Codex did not default to Chinese.'
[ "$(grep -Ec '^localeOverride = "zh-CN"$' "$config")" = 1 ] || fail 'Chinese locale override is duplicated or nested.'
[ "$(grep -n '^localeOverride = "zh-CN"$' "$config" | cut -d: -f1)" -lt "$(grep -n '^\[' "$config" | head -n 1 | cut -d: -f1)" ] || fail 'Chinese locale override is not a root key.'
grep -Fq 'network_access = true' "$config" || fail 'Image skill network access was not enabled.'
grep -Fq 'sk-clean-macos-test' "$config" || fail 'OAuth bearer token was not written for the mature OAuth path.'
[ "$(shasum -a 256 "$auth" | awk '{print $1}')" = "$auth_hash" ] || fail 'OAuth state changed.'
[ "$(shasum -a 256 "$session" | awk '{print $1}')" = "$session_hash" ] || fail 'Session data changed.'

MADAPI_API_KEY='sk-refresh-macos' \
MADAPI_CODEX_AUTH_KIND=apikey \
MADAPI_REFRESH_RESPONSE_FILE="$repo_root/tests/fixtures/newapi-models.json" \
MADAPI_CODEX_TEMPLATE_FILE="$templates" \
CODEX_HOME="$codex_home" \
/bin/sh "$refresh_path"

grep -Fq '"slug": "gpt-5.3-codex"' "$codex_home/madapi-cockpit-model-catalog.json" || fail 'Sol Pro compatibility slug is missing.'
grep -Fq '"slug": "gpt-5.2"' "$codex_home/madapi-cockpit-model-catalog.json" || fail 'Terra Pro compatibility slug is missing.'
! grep -Fq '"slug": "gpt-image-2"' "$codex_home/madapi-cockpit-model-catalog.json" || fail 'Image-only model leaked into the conversation selector.'

oauth_auth="$codex_home/auth.json"
printf '%s' '{"auth_mode":"chatgpt","tokens":{"access_token":"oauth-access","refresh_token":"oauth-refresh"}}' > "$oauth_auth"
MADAPI_CODEX_AUTH_KIND=oauth \
MADAPI_API_KEY='sk-refresh-macos' \
MADAPI_REFRESH_RESPONSE_FILE="$oauth_models" \
MADAPI_CODEX_TEMPLATE_FILE="$asset_root/codex-model-templates.json" \
CODEX_HOME="$codex_home" \
/bin/sh "$refresh_path"
oauth_count=$(node -e 'const fs=require("fs");const p=JSON.parse(fs.readFileSync(process.argv[1],"utf8"));process.stdout.write(String(p.models.length))' "$codex_home/madapi-cockpit-model-catalog.json")
[ "$oauth_count" = 17 ] || fail 'OAuth catalog does not contain exactly 17 conversation models.'
grep -Fq '"slug": "grok-4.6"' "$codex_home/madapi-cockpit-model-catalog.json" || fail 'OAuth catalog is missing grok-4.6.'
! grep -Fq '"slug": "gpt-image-2"' "$codex_home/madapi-cockpit-model-catalog.json" || fail 'OAuth image model leaked into the conversation selector.'
! grep -Fq '"slug": "seedance-2.0-fast"' "$codex_home/madapi-cockpit-model-catalog.json" || fail 'OAuth video model leaked into the conversation selector.'
! grep -Fq '"token_budget"' "$codex_home/madapi-cockpit-model-catalog.json" || fail 'Unsupported token-budget lifecycle leaked into the Codex catalog.'

api_home="$root/api-codex"
mkdir -p "$api_home/sessions"
printf '%s' 'model_provider = "custom"\n\n[model_providers.custom]\nname = "custom"\nbase_url = "https://old.invalid/v1"\n' > "$api_home/config.toml"
printf '%s' '{"auth_mode":"apikey","OPENAI_API_KEY":"old-api-key"}' > "$api_home/auth.json"
CODEX_HOME="$api_home" \
MADAPI_KEY='sk-clean-macos-api-test' \
MADAPI_BASE_URL='http://127.0.0.1:13016' \
MADAPI_CODEX_LOGIN_MODE='apikey' \
MADAPI_INSTALL_TEST_MODE=1 \
MADAPI_REFRESH_SCRIPT_SOURCE="$refresh_path" \
MADAPI_HISTORY_RESTORE_SCRIPT_SOURCE="$history_path" \
MADAPI_REFRESH_RESPONSE_FILE="$repo_root/tests/fixtures/newapi-models.json" \
MADAPI_CODEX_TEMPLATE_FILE="$repo_root/tests/fixtures/codex-model-templates.json" \
MADAPI_IMAGE_SKILL_SOURCE_DIR="$asset_root/image-skill" \
/bin/sh "$installer_path"
grep -Fq 'requires_openai_auth = false' "$api_home/config.toml" || fail 'API gateway auth mode is missing.'
grep -Fq 'base_url = "http://127.0.0.1:13016/codex/cockpit/v1"' "$api_home/config.toml" || fail 'API compatibility route is missing.'
grep -Fq 'env_key = "MADAPI_API_KEY"' "$api_home/config.toml" || fail 'API gateway env_key is missing.'
! grep -Fq 'experimental_bearer_token' "$api_home/config.toml" || fail 'API config contains OAuth bearer authentication.'
grep -Fq 'image_generation = true' "$api_home/config.toml" || fail 'API image generation was not enabled.'
grep -Fq 'localeOverride = "zh-CN"' "$api_home/config.toml" || fail 'API Codex did not default to Chinese.'
[ "$(grep -Ec '^localeOverride = "zh-CN"$' "$api_home/config.toml")" = 1 ] || fail 'API Chinese locale override is duplicated or nested.'
[ "$(grep -n '^localeOverride = "zh-CN"$' "$api_home/config.toml" | cut -d: -f1)" -lt "$(grep -n '^\[' "$api_home/config.toml" | head -n 1 | cut -d: -f1)" ] || fail 'API Chinese locale override is not a root key.'
grep -Fq 'network_access = true' "$api_home/config.toml" || fail 'API image skill network access was not enabled.'
! grep -Fq 'MADAPI_CODEX_LANGUAGE' "$installer_path" || fail 'Codex installer still exposes an English language option.'
api_count=$(node -e 'const fs=require("fs");const p=JSON.parse(fs.readFileSync(process.argv[1],"utf8"));process.stdout.write(String(p.models.length))' "$api_home/madapi-cockpit-model-catalog.json")
[ "$api_count" = 8 ] || fail 'API catalog does not contain exactly eight models.'
grep -Fq '"display_name": "grok-4.6"' "$api_home/madapi-cockpit-model-catalog.json" || fail 'API catalog is missing grok-4.6.'
! grep -Fq '"display_name": "grok-4.5"' "$api_home/madapi-cockpit-model-catalog.json" || fail 'API catalog still contains grok-4.5.'

api_auth_hash=$(shasum -a 256 "$api_home/auth.json" | awk '{print $1}')
CODEX_HOME="$api_home" \
MADAPI_KEY='sk-clean-macos-oauth-test' \
MADAPI_BASE_URL='http://127.0.0.1:13016' \
MADAPI_CODEX_LOGIN_MODE='oauth' \
MADAPI_INSTALL_TEST_MODE=1 \
MADAPI_REFRESH_SCRIPT_SOURCE="$refresh_path" \
MADAPI_HISTORY_RESTORE_SCRIPT_SOURCE="$history_path" \
MADAPI_REFRESH_RESPONSE_FILE="$oauth_models" \
MADAPI_CODEX_TEMPLATE_FILE="$templates" \
MADAPI_IMAGE_SKILL_SOURCE_DIR="$asset_root/image-skill" \
/bin/sh "$installer_path"
[ ! -e "$api_home/auth.json" ] || fail 'OAuth mode did not exit API-key login.'
api_auth_backup=$(find "$api_home" -maxdepth 1 -name 'auth.json.madapi-backup-*' -type f | sort | tail -n 1)
[ -n "$api_auth_backup" ] || fail 'API authentication backup is missing.'
[ "$(shasum -a 256 "$api_auth_backup" | awk '{print $1}')" = "$api_auth_hash" ] || fail 'API authentication backup is not exact.'
grep -Fq 'base_url = "http://127.0.0.1:13016/codex/v1"' "$api_home/config.toml" || fail 'OAuth switch did not select the native route.'
grep -Fq 'requires_openai_auth = true' "$api_home/config.toml" || fail 'OAuth switch auth gate is wrong.'
grep -Fq 'experimental_bearer_token = ' "$api_home/config.toml" || fail 'OAuth switch bearer token is missing.'
! grep -Fq 'env_key = "MADAPI_API_KEY"' "$api_home/config.toml" || fail 'OAuth switch retained API env_key.'
grep -Fq 'localeOverride = "zh-CN"' "$api_home/config.toml" || fail 'OAuth switch lost the Chinese default.'
grep -Fq 'image_generation = true' "$api_home/config.toml" || fail 'OAuth switch lost image generation.'
oauth_switch_count=$(node -e 'const fs=require("fs");const p=JSON.parse(fs.readFileSync(process.argv[1],"utf8"));process.stdout.write(String(p.models.length))' "$api_home/madapi-cockpit-model-catalog.json")
[ "$oauth_switch_count" = 17 ] || fail 'OAuth switch catalog does not contain exactly 17 conversation models.'

printf '%s\n' 'Codex macOS clean gateway acceptance passed.'
