#!/bin/sh
set -eu

api_key=${MADAPI_KEY:-}
case "$api_key" in
  sk-*) ;;
  *) printf '%s\n' 'MADAPI_KEY is missing or invalid.' >&2; exit 1 ;;
esac
case "$api_key" in
  *[!A-Za-z0-9._-]*)
    printf '%s\n' 'MADAPI_KEY contains unsupported characters.' >&2
    exit 1
    ;;
esac

codex_home=${CODEX_HOME:-"$HOME/.codex"}
config_path="$codex_home/config.toml"
temp_path="$codex_home/config.toml.madapi.tmp"
body_path="$codex_home/config.toml.madapi.body"
backup_path=

umask 077
mkdir -p "$codex_home"

if [ -f "$config_path" ]; then
  backup_path="$config_path.madapi-backup-$(date '+%Y%m%d-%H%M%S')"
  cp "$config_path" "$backup_path"
  awk '
    BEGIN { current = ""; skip = 0 }
    /^[[:space:]]*\[[^]]+\][[:space:]]*(#.*)?$/ {
      section = $0
      sub(/^[[:space:]]*\[/, "", section)
      sub(/\].*$/, "", section)
      current = section
      skip = (current == "model_providers.madapi" || index(current, "model_providers.madapi.") == 1)
      if (skip) next
    }
    skip { next }
    current == "" && /^[[:space:]]*(model_provider|model)[[:space:]]*=/ { next }
    { print }
  ' "$config_path" > "$body_path"
else
  : > "$body_path"
fi

{
  printf '%s\n' 'model_provider = "madapi"'
  printf '%s\n\n' 'model = "gpt-5.6-sol"'
  cat "$body_path"
  printf '\n%s\n' '[model_providers.madapi]'
  printf '%s\n' 'name = "MadAPI"'
  printf '%s\n' 'base_url = "https://mad.myddns.me/codex/v1"'
  printf '%s\n' 'wire_api = "responses"'
  printf '%s\n' 'stream_idle_timeout_ms = 360000'
  printf '%s\n' 'request_max_retries = 0'
  printf '%s\n\n' 'context_window_override = 1048576'
  printf '%s\n' '[model_providers.madapi.auth]'
  printf '%s\n' 'command = "/bin/sh"'
  printf 'args = ["-c", "printf %%s '\''%s'\''"]\n' "$api_key"
  printf '%s\n' 'timeout_ms = 5000'
  printf '%s\n' 'refresh_interval_ms = 300000'
} > "$temp_path"

mv "$temp_path" "$config_path"
rm -f "$body_path"
chmod 600 "$config_path"

printf 'MadAPI Codex configuration installed: %s\n' "$config_path"
if [ -n "$backup_path" ]; then
  printf 'Backup created: %s\n' "$backup_path"
fi
printf '%s\n' 'Restart Codex to load the model catalog.'
