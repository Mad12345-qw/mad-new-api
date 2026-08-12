#!/bin/sh
set -eu

installer_path=${1:?installer path is required}
asset_root=$(CDPATH= cd -- "$(dirname -- "$installer_path")" && pwd)
repo_root=$(CDPATH= cd -- "$asset_root/../../.." && pwd)
refresh_path=$asset_root/refresh-model-catalog.sh
history_path=$asset_root/restore-history.sh
oauth_models=$repo_root/tests/fixtures/oauth-codex-models.json
api_models=$repo_root/tests/fixtures/newapi-models.json
templates=$repo_root/tests/fixtures/cpa-codex-templates.json

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
grep -Fq '/codex/v1' "$installer_path" || fail 'Dedicated NewAPI-CPA route is missing.'
! grep -Fq '/codex/cockpit/v1' "$installer_path" || fail 'Legacy API compatibility route remains.'

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

grep -Fq 'base_url = "http://127.0.0.1:13016/codex/v1"' "$config" || fail 'Dedicated NewAPI-CPA route is missing.'
grep -Fq 'requires_openai_auth = true' "$config" || fail 'OAuth gateway auth mode changed.'
grep -Fq 'experimental_bearer_token = ' "$config" || fail 'OAuth bearer token is missing.'
! grep -Fq 'env_key = "MADAPI_API_KEY"' "$config" || fail 'OAuth config incorrectly uses API env_key.'
grep -Fq 'image_generation = true' "$config" || fail 'Image generation was not enabled.'
grep -Fq 'localeOverride = "zh-CN"' "$config" || fail 'Chinese Codex interface was not enabled.'
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
MADAPI_CODEX_TEMPLATE_FILE="$repo_root/tests/fixtures/cpa-codex-templates.json" \
CODEX_HOME="$codex_home" \
/bin/sh "$refresh_path"
oauth_count=$(node -e 'const fs=require("fs");const p=JSON.parse(fs.readFileSync(process.argv[1],"utf8"));process.stdout.write(String(p.models.length))' "$codex_home/madapi-cockpit-model-catalog.json")
[ "$oauth_count" = 17 ] || fail 'OAuth catalog does not contain exactly 17 conversation models.'
grep -Fq '"slug": "grok-4.6"' "$codex_home/madapi-cockpit-model-catalog.json" || fail 'OAuth catalog is missing grok-4.6.'
! grep -Fq '"slug": "gpt-image-2"' "$codex_home/madapi-cockpit-model-catalog.json" || fail 'OAuth image model leaked into the conversation selector.'
! grep -Fq '"slug": "seedance-2.0-fast"' "$codex_home/madapi-cockpit-model-catalog.json" || fail 'OAuth video model leaked into the conversation selector.'

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
MADAPI_CODEX_TEMPLATE_FILE="$repo_root/tests/fixtures/cpa-codex-templates.json" \
MADAPI_IMAGE_SKILL_SOURCE_DIR="$asset_root/image-skill" \
/bin/sh "$installer_path"
grep -Fq 'requires_openai_auth = false' "$api_home/config.toml" || fail 'API gateway auth mode is missing.'
grep -Fq 'env_key = "MADAPI_API_KEY"' "$api_home/config.toml" || fail 'API gateway env_key is missing.'
! grep -Fq 'experimental_bearer_token' "$api_home/config.toml" || fail 'API config contains OAuth bearer authentication.'
grep -Fq 'image_generation = true' "$api_home/config.toml" || fail 'API image generation was not enabled.'
grep -Fq 'localeOverride = "zh-CN"' "$api_home/config.toml" || fail 'API Chinese Codex interface was not enabled.'
grep -Fq 'network_access = true' "$api_home/config.toml" || fail 'API image skill network access was not enabled.'
api_count=$(node -e 'const fs=require("fs");const p=JSON.parse(fs.readFileSync(process.argv[1],"utf8"));process.stdout.write(String(p.models.length))' "$api_home/madapi-cockpit-model-catalog.json")
[ "$api_count" = 8 ] || fail 'API catalog does not contain exactly eight models.'
grep -Fq '"display_name": "grok-4.6"' "$api_home/madapi-cockpit-model-catalog.json" || fail 'API catalog is missing grok-4.6.'
! grep -Fq '"display_name": "grok-4.5"' "$api_home/madapi-cockpit-model-catalog.json" || fail 'API catalog still contains grok-4.5.'

printf '%s\n' 'Codex macOS clean gateway acceptance passed.'
