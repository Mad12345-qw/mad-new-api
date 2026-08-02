#!/bin/sh
set -eu

api_key=${MADAPI_KEY:-}
case "$api_key" in
  sk-*[!A-Za-z0-9._-]*|'') printf '%s\n' 'MADAPI_KEY is missing or invalid.' >&2; exit 1 ;;
  sk-*) ;;
  *) printf '%s\n' 'MADAPI_KEY is missing or invalid.' >&2; exit 1 ;;
esac

codex_home=${CODEX_HOME:-"$HOME/.codex"}
config_path="$codex_home/config.toml"
auth_path="$codex_home/auth.json"
models_cache_path="$codex_home/models_cache.json"
transaction_id="$$-$(date '+%s')"
temp_path="$codex_home/config.toml.madapi.$transaction_id.tmp"
body_path="$codex_home/config.toml.madapi.$transaction_id.body"
backup_path=
had_config=0
config_installed=0
success=0

umask 077
[ -f "$auth_path" ] || { printf '%s\n' 'Sign in with ChatGPT in Codex Desktop before running this installer. No files were changed.' >&2; exit 1; }
oauth_mode=$(/usr/bin/plutil -extract auth_mode raw -o - "$auth_path" 2>/dev/null || true)
oauth_access_token=$(/usr/bin/plutil -extract tokens.access_token raw -o - "$auth_path" 2>/dev/null || true)
oauth_refresh_token=$(/usr/bin/plutil -extract tokens.refresh_token raw -o - "$auth_path" 2>/dev/null || true)
if [ "$oauth_mode" != 'chatgpt' ] || [ -z "$oauth_access_token" ] || [ "$oauth_access_token" = 'null' ] || [ -z "$oauth_refresh_token" ] || [ "$oauth_refresh_token" = 'null' ]; then
  printf '%s\n' 'This installer supports ChatGPT OAuth sessions only. Sign in with ChatGPT in Codex Desktop first. No files were changed.' >&2
  exit 1
fi

mkdir -p "$codex_home"
[ -f "$config_path" ] && had_config=1

root_string_value() {
  key=$1
  file=$2
  awk -v key="$key" '
    /^[[:space:]]*\[/ { exit }
    $0 ~ "^[[:space:]]*" key "[[:space:]]*=" {
      line = $0
      sub(/^[^=]*=[[:space:]]*"/, "", line)
      sub(/".*/, "", line)
      print line
      exit
    }
  ' "$file"
}

provider_display_name() {
  provider=$1
  file=$2
  awk -v target="model_providers.$provider" '
    /^[[:space:]]*\[[^]]+\][[:space:]]*(#.*)?$/ {
      section = $0
      sub(/^[[:space:]]*\[/, "", section)
      sub(/\].*$/, "", section)
      current = section
      next
    }
    current == target && /^[[:space:]]*name[[:space:]]*=/ {
      line = $0
      sub(/^[^=]*=[[:space:]]*"/, "", line)
      sub(/".*/, "", line)
      print line
      exit
    }
  ' "$file"
}

toml_string() {
  printf '%s' "$1" | awk '{ gsub(/\\/, "\\\\"); gsub(/"/, "\\\""); printf "\"%s\"", $0 }'
}

provider_source=$config_path
provider_id=
if [ "$had_config" -eq 1 ]; then
  provider_id=$(root_string_value model_provider "$config_path")
  if [ -z "$provider_id" ] || [ "$provider_id" = 'madapi' ]; then
    for recovery_path in "$config_path".madapi-backup-*; do
      [ -f "$recovery_path" ] || continue
      recovery_provider=$(root_string_value model_provider "$recovery_path")
      if [ -n "$recovery_provider" ] && [ "$recovery_provider" != 'madapi' ]; then
        provider_id=$recovery_provider
        provider_source=$recovery_path
        break
      fi
    done
  fi
fi
case "$provider_id" in
  ''|openai|ollama|lmstudio|amazon-bedrock) provider_id=custom; provider_source= ;;
esac
case "$provider_id" in
  *[!A-Za-z0-9_-]*) printf '%s\n' 'The existing model provider identifier is not supported. No files were changed.' >&2; exit 1 ;;
esac
provider_name=
[ -f "$provider_source" ] && provider_name=$(provider_display_name "$provider_id" "$provider_source")
[ -n "$provider_name" ] || provider_name=$provider_id

if [ "$had_config" -eq 1 ]; then
  LC_ALL=C awk -v target="model_providers.$provider_id" '
    function root_key(line, equals_at, key, first, last, quote) {
      equals_at = index(line, "=")
      if (equals_at == 0) return ""
      key = substr(line, 1, equals_at - 1)
      sub(/^[[:space:]]+/, "", key)
      sub(/[[:space:]]+$/, "", key)
      if (length(key) >= 2) {
        first = substr(key, 1, 1); last = substr(key, length(key), 1); quote = sprintf("%c", 39)
        if ((first == "\"" && last == "\"") || (first == quote && last == quote)) key = substr(key, 2, length(key) - 2)
      }
      return key
    }
    BEGIN { current = ""; skip = 0 }
    /^[[:space:]]*\[[^]]+\][[:space:]]*(#.*)?$/ {
      section = $0; sub(/^[[:space:]]*\[/, "", section); sub(/\].*$/, "", section); current = section
      skip = (current == "model_providers.madapi" || index(current, "model_providers.madapi.") == 1 || current == target || index(current, target ".") == 1)
      if (skip) next
    }
    skip { next }
    current == "" { key = root_key($0); if (key == "model_provider" || key == "model_catalog_json") next }
    { print }
  ' "$config_path" > "$body_path"
else
  : > "$body_path"
fi

cleanup() {
  if [ "$success" -ne 1 ] && [ "$config_installed" -eq 1 ]; then
    if [ "$had_config" -eq 1 ] && [ -n "$backup_path" ] && [ -f "$backup_path" ]; then cp "$backup_path" "$config_path"; else rm -f "$config_path"; fi
  fi
  rm -f "$temp_path" "$body_path"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

{
  printf 'model_provider = %s\n' "$(toml_string "$provider_id")"
  if [ "$had_config" -eq 0 ]; then
    printf '%s\n' 'model = "gpt-5.6-sol"'
    printf '%s\n' 'model_reasoning_effort = "high"'
    printf '%s\n' 'model_auto_compact_token_limit = 500000'
  fi
  printf '\n'
  cat "$body_path"
  printf '\n[model_providers.%s]\n' "$provider_id"
  printf 'name = %s\n' "$(toml_string "$provider_name")"
  printf '%s\n' 'base_url = "https://mad.myddns.me/codex/v1"'
  printf '%s\n' 'wire_api = "responses"'
  printf '%s\n' 'requires_openai_auth = true'
  printf 'experimental_bearer_token = %s\n' "$(toml_string "$api_key")"
  printf '%s\n' 'stream_idle_timeout_ms = 360000'
  printf '%s\n' 'request_max_retries = 3'
  printf '%s\n' 'context_window_override = 1048576'
} > "$temp_path"

if [ "$had_config" -eq 1 ]; then
  backup_path="$config_path.madapi-backup-$(date '+%Y%m%d-%H%M%S')-$$"
  cp -p "$config_path" "$backup_path"
fi
mv "$temp_path" "$config_path"
config_installed=1
chmod 600 "$config_path"
rm -f "$models_cache_path"
success=1

printf 'MadAPI Codex desktop configuration installed: %s\n' "$config_path"
[ -z "$backup_path" ] || printf 'Backup created: %s\n' "$backup_path"
printf '%s\n' 'Existing ChatGPT OAuth session preserved.'
printf '%s\n' 'Restart Codex Desktop to refresh the model list.'
