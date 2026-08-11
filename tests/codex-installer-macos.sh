#!/bin/sh
set -eu

installer_path=${1:?installer path is required}
asset_root=$(CDPATH= cd -- "$(dirname -- "$installer_path")" && pwd)
repo_root=$(CDPATH= cd -- "$asset_root/../../.." && pwd)
refresh_path=$asset_root/refresh-model-catalog.sh
history_path=$asset_root/restore-history.sh

fail() { printf '%s\n' "$1" >&2; exit 1; }

/bin/sh -n "$installer_path"
/bin/sh -n "$refresh_path"
/bin/sh -n "$history_path"
grep -Fq 'requires_openai_auth = false' "$installer_path" || fail 'Official custom gateway authentication is missing.'
grep -Fq 'env_key = ' "$installer_path" || fail 'Official custom gateway env_key is missing.'
grep -Fq 'x-openai-actor-authorization' "$installer_path" || fail 'Official gateway actor header is missing.'
grep -Fq 'supports_websockets = false' "$installer_path" || fail 'WebSocket opt-out is missing.'
grep -Fq 'image_generation = true' "$installer_path" || fail 'Codex image generation feature is missing.'
grep -Fq '/codex/v1' "$installer_path" || fail 'Dedicated NewAPI-CPA route is missing.'
! grep -Fq '/codex/cockpit/v1' "$installer_path" || fail 'Legacy API compatibility route remains.'
! grep -Fq 'experimental_bearer_token' "$installer_path" || fail 'Token is still written into config.toml.'

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
/bin/sh "$installer_path"

grep -Fq 'base_url = "http://127.0.0.1:13016/codex/v1"' "$config" || fail 'Dedicated NewAPI-CPA route is missing.'
grep -Fq 'requires_openai_auth = false' "$config" || fail 'Official gateway auth mode changed.'
grep -Fq 'env_key = "MADAPI_API_KEY"' "$config" || fail 'Gateway env key is missing.'
grep -Fq 'image_generation = true' "$config" || fail 'Image generation was not enabled.'
! grep -Fq 'sk-clean-macos-test' "$config" || fail 'MadAPI key leaked into config.toml.'
[ "$(shasum -a 256 "$auth" | awk '{print $1}')" = "$auth_hash" ] || fail 'OAuth state changed.'
[ "$(shasum -a 256 "$session" | awk '{print $1}')" = "$session_hash" ] || fail 'Session data changed.'

MADAPI_API_KEY='sk-refresh-macos' \
MADAPI_REFRESH_RESPONSE_FILE="$repo_root/tests/fixtures/newapi-models.json" \
MADAPI_CODEX_TEMPLATE_FILE="$repo_root/tests/fixtures/cpa-codex-templates.json" \
CODEX_HOME="$codex_home" \
/bin/sh "$refresh_path"

grep -Fq '"slug": "gpt-5.6-sol-pro"' "$codex_home/madapi-cockpit-model-catalog.json" || fail 'Sol Pro is missing.'
grep -Fq '"slug": "gpt-5.6-terra-pro"' "$codex_home/madapi-cockpit-model-catalog.json" || fail 'Terra Pro is missing.'
! grep -Fq '"slug": "gpt-image-2"' "$codex_home/madapi-cockpit-model-catalog.json" || fail 'Image-only model leaked into the conversation selector.'

printf '%s\n' 'Codex macOS clean gateway acceptance passed.'
