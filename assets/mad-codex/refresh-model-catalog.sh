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
curl -fsS -H "Authorization: Bearer $key" 'https://mad.myddns.me/codex/cockpit/v1/models' -o "$temp_path"
/usr/bin/plutil -lint "$temp_path" >/dev/null
/usr/bin/plutil -extract models.0.slug raw -o - "$temp_path" >/dev/null 2>&1 || { printf '%s\n' 'MadAPI returned an empty Codex model catalog' >&2; exit 1; }
mv -f "$temp_path" "$catalog_path"
trap - EXIT
printf '%s\n' 'MadAPI Codex model catalog refreshed.'
