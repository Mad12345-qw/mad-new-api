#!/bin/sh
set -eu

codex_home=${CODEX_HOME:-$HOME/.codex}
config_path=$codex_home/config.toml
auth_path=$codex_home/auth.json
catalog_path=$codex_home/madapi-cockpit-model-catalog.json
models_cache_path=$codex_home/models_cache.json
[ -f "$config_path" ] || exit 0
grep -Eq 'https://mad\.myddns\.me/codex/(cockpit/)?v1' "$config_path" || exit 0

provider=$(awk '
  /^[[:space:]]*\[/ { exit }
  /^[[:space:]]*model_provider[[:space:]]*=/ {
    line=$0; sub(/^[^=]*=[[:space:]]*"/, "", line); sub(/".*/, "", line); print line; exit
  }
' "$config_path")
[ -n "$provider" ] || { printf '%s\n' 'Active model provider not found in config.toml' >&2; exit 1; }
token_line=$(awk -v target="model_providers.$provider" '
  /^[[:space:]]*\[[^]]+\][[:space:]]*(#.*)?$/ {
    section=$0; sub(/^[[:space:]]*\[/, "", section); sub(/\].*$/, "", section); current=section; next
  }
  current == target && /^[[:space:]]*experimental_bearer_token[[:space:]]*=/ { print; exit }
' "$config_path")
key=$(printf '%s\n' "$token_line" | sed -nE 's/^[[:space:]]*experimental_bearer_token[[:space:]]*=[[:space:]]*"([^"]+)"[[:space:]]*$/\1/p')
if [ -z "$key" ]; then
  auth_line=$(awk -v target="model_providers.$provider.auth" '
    /^[[:space:]]*\[[^]]+\][[:space:]]*(#.*)?$/ {
      section=$0; sub(/^[[:space:]]*\[/, "", section); sub(/\].*$/, "", section); current=section; next
    }
    current == target && /^[[:space:]]*args[[:space:]]*=/ { print; exit }
  ' "$config_path")
  key=$(printf '%s\n' "$auth_line" | sed -nE "s/.*printf %s '([^']+)'.*/\1/p")
fi
[ -n "$key" ] || { printf '%s\n' 'MadAPI key not found in config.toml' >&2; exit 1; }

auth_kind=${MADAPI_CODEX_AUTH_KIND:-}
if [ -z "$auth_kind" ]; then
  auth_kind=unconfigured
  if [ -f "$auth_path" ]; then
    auth_kind=$(AUTH_PATH="$auth_path" /usr/bin/osascript -l JavaScript <<'JXA'
ObjC.import('Foundation')
var path = ObjC.unwrap($.NSProcessInfo.processInfo.environment.objectForKey('AUTH_PATH'))
var data = $.NSData.dataWithContentsOfFile(path)
if (!data) throw new Error('Authentication file is unreadable')
var text = ObjC.unwrap($.NSString.alloc.initWithDataEncoding(data, $.NSUTF8StringEncoding))
var auth = JSON.parse(text)
var tokens = auth && auth.tokens && typeof auth.tokens === 'object' ? auth.tokens : {}
var result
if (auth.auth_mode !== 'apikey' && typeof tokens.access_token === 'string' && tokens.access_token && typeof tokens.refresh_token === 'string' && tokens.refresh_token) {
  result = 'oauth'
} else if (auth.auth_mode === 'apikey' || (typeof auth.OPENAI_API_KEY === 'string' && auth.OPENAI_API_KEY)) {
  result = 'apikey'
} else {
  result = 'unconfigured'
}
result
JXA
    ) || { printf '%s\n' 'Codex Desktop authentication state is unreadable.' >&2; exit 1; }
  fi
fi
case "$auth_kind" in oauth|apikey|unconfigured) ;; *) printf '%s\n' 'MADAPI_CODEX_AUTH_KIND is invalid.' >&2; exit 1 ;; esac

if [ "$auth_kind" = apikey ]; then
  catalog_url=https://mad.myddns.me/codex/cockpit/v1/models
  desired_base_url=https://mad.myddns.me/codex/cockpit/v1
  desired_auth=false
else
  catalog_url=https://mad.myddns.me/codex/v1/models
  desired_base_url=https://mad.myddns.me/codex/v1
  desired_auth=true
fi

mkdir -p "$codex_home"
config_temp_path=$codex_home/config.toml.madapi-refresh.$$.tmp
temp_path=$codex_home/madapi-cockpit-model-catalog.$$.tmp
trap 'rm -f "$config_temp_path" "$temp_path"' EXIT
awk -v target="model_providers.$provider" -v base_url="$desired_base_url" -v require_auth="$desired_auth" '
  /^[[:space:]]*\[[^]]+\][[:space:]]*(#.*)?$/ {
    section=$0; sub(/^[[:space:]]*\[/, "", section); sub(/\].*$/, "", section); current=section
  }
  current == target && /^[[:space:]]*base_url[[:space:]]*=/ { print "base_url = \"" base_url "\""; next }
  current == target && /^[[:space:]]*requires_openai_auth[[:space:]]*=/ { print "requires_openai_auth = " require_auth; next }
  { print }
' "$config_path" > "$config_temp_path"
config_changed=0
if ! cmp -s "$config_path" "$config_temp_path"; then
  mv -f "$config_temp_path" "$config_path"
  config_changed=1
else
  rm -f "$config_temp_path"
fi

if [ -n "${MADAPI_REFRESH_RESPONSE_FILE:-}" ]; then
  cp "$MADAPI_REFRESH_RESPONSE_FILE" "$temp_path"
else
  curl -fsS -H "Authorization: Bearer $key" "$catalog_url" -o "$temp_path"
fi
if ! CATALOG_PATH="$temp_path" /usr/bin/osascript -l JavaScript <<'JXA' >/dev/null 2>&1
ObjC.import('Foundation')
var path = ObjC.unwrap($.NSProcessInfo.processInfo.environment.objectForKey('CATALOG_PATH'))
var data = $.NSData.dataWithContentsOfFile(path)
if (!data) throw new Error('Catalog file is unreadable')
var text = ObjC.unwrap($.NSString.alloc.initWithDataEncoding(data, $.NSUTF8StringEncoding))
var catalog = JSON.parse(text)
if (!catalog || !Array.isArray(catalog.models) || catalog.models.length < 1) throw new Error('Catalog is empty')
if (typeof catalog.models[0].slug !== 'string' || catalog.models[0].slug.length < 1) throw new Error('Catalog model slug is missing')
JXA
then
  printf '%s\n' 'MadAPI returned an invalid or empty Codex model catalog' >&2
  exit 1
fi
mv -f "$temp_path" "$catalog_path"
rm -f "$models_cache_path"
trap - EXIT
printf 'MadAPI Codex model catalog refreshed: %s\n' "$auth_kind"
