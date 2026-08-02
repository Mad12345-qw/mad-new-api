#!/bin/sh
set -eu

codex_home=${CODEX_HOME:-$HOME/.codex}
config_path=$codex_home/config.toml
catalog_path=$codex_home/madapi-cockpit-model-catalog.json
[ -f "$config_path" ] || exit 0
grep -Fq 'https://mad.myddns.me/codex/cockpit/v1' "$config_path" || exit 0

provider=$(awk '
  /^[[:space:]]*\[/ { exit }
  /^[[:space:]]*model_provider[[:space:]]*=/ {
    line=$0; sub(/^[^=]*=[[:space:]]*"/, "", line); sub(/".*/, "", line); print line; exit
  }
' "$config_path")
[ -n "$provider" ] || { printf '%s\n' 'Active model provider not found in config.toml' >&2; exit 1; }
auth_line=$(awk -v target="model_providers.$provider.auth" '
  /^[[:space:]]*\[[^]]+\][[:space:]]*(#.*)?$/ {
    section=$0; sub(/^[[:space:]]*\[/, "", section); sub(/\].*$/, "", section); current=section; next
  }
  current == target && /^[[:space:]]*args[[:space:]]*=/ { print; exit }
' "$config_path")
key=$(printf '%s\n' "$auth_line" | sed -nE "s/.*printf %s '([^']+)'.*/\1/p")
if [ -z "$key" ]; then
  token_line=$(awk -v target="model_providers.$provider" '
    /^[[:space:]]*\[[^]]+\][[:space:]]*(#.*)?$/ {
      section=$0; sub(/^[[:space:]]*\[/, "", section); sub(/\].*$/, "", section); current=section; next
    }
    current == target && /^[[:space:]]*experimental_bearer_token[[:space:]]*=/ { print; exit }
  ' "$config_path")
  key=$(printf '%s\n' "$token_line" | sed -nE 's/^[[:space:]]*experimental_bearer_token[[:space:]]*=[[:space:]]*"([^"]+)"[[:space:]]*$/\1/p')
fi
[ -n "$key" ] || { printf '%s\n' 'MadAPI key not found in config.toml' >&2; exit 1; }

mkdir -p "$codex_home"
temp_path=$codex_home/madapi-cockpit-model-catalog.$$.tmp
trap 'rm -f "$temp_path"' EXIT
if [ -n "${MADAPI_REFRESH_RESPONSE_FILE:-}" ]; then
  cp "$MADAPI_REFRESH_RESPONSE_FILE" "$temp_path"
else
  curl -fsS -H "Authorization: Bearer $key" 'https://mad.myddns.me/codex/cockpit/v1/models' -o "$temp_path"
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
trap - EXIT
printf '%s\n' 'MadAPI Codex model catalog refreshed.'
