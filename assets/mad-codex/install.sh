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
key_path="$codex_home/madapi.key"
models_cache_path="$codex_home/models_cache.json"
transaction_id="$$-$(date '+%s')"
temp_path="$codex_home/config.toml.madapi.$transaction_id.tmp"
body_path="$codex_home/config.toml.madapi.$transaction_id.body"
temp_key_path="$codex_home/madapi.key.$transaction_id.tmp"
rollback_key_path="$codex_home/madapi.key.$transaction_id.rollback"
backup_path=
had_config=0
had_key=0
config_installed=0
key_installed=0
success=0

umask 077
mkdir -p "$codex_home"
[ -f "$config_path" ] && had_config=1
[ -f "$key_path" ] && had_key=1

find_codex_cli() {
  candidates=
  if [ -n "${CODEX_CLI_PATH:-}" ]; then
    candidates=$CODEX_CLI_PATH
  fi
  path_codex=$(command -v codex 2>/dev/null || true)
  if [ -n "$path_codex" ]; then
    candidates="$candidates
$path_codex"
  fi
  candidates="$candidates
/Applications/Codex.app/Contents/Resources/codex
/Applications/ChatGPT.app/Contents/Resources/codex
$HOME/Library/Application Support/OpenAI/Codex/bin/codex"

  old_ifs=$IFS
  IFS='
'
  for candidate in $candidates; do
    [ -n "$candidate" ] || continue
    [ -x "$candidate" ] || continue
    if "$candidate" --version >/dev/null 2>&1; then
      IFS=$old_ifs
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  IFS=$old_ifs
  return 1
}

codex_config_valid() {
  CODEX_HOME="$codex_home" "$codex_cli" features list >/dev/null 2>&1
}

codex_cli=$(find_codex_cli || true)
if [ -z "$codex_cli" ]; then
  printf '%s\n' 'Codex CLI was not found. Install or update Codex, then try again.' >&2
  exit 1
fi
if [ "$had_config" -eq 1 ] && ! codex_config_valid; then
  printf '%s\n' 'The existing Codex configuration could not be parsed. No files were changed.' >&2
  exit 1
fi

has_root_key() {
  key=$1
  awk -v key="$key" '
    BEGIN { found = 0 }
    /^[[:space:]]*\[/ { exit }
    $0 ~ "^[[:space:]]*" key "[[:space:]]*=" { found = 1 }
    END { exit(found ? 0 : 1) }
  ' "$config_path"
}

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

has_reasoning=0
has_compact=0
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
      fi
    done
  fi
  has_root_key model_reasoning_effort && has_reasoning=1
  has_root_key model_auto_compact_token_limit && has_compact=1
fi
case "$provider_id" in
  ''|openai|ollama|lmstudio|amazon-bedrock)
    provider_id=custom
    provider_source=
    ;;
esac
case "$provider_id" in
  *[!A-Za-z0-9_-]*)
    printf '%s\n' 'The existing model provider identifier is not supported. No files were changed.' >&2
    exit 1
    ;;
esac
provider_name=
if [ -f "$provider_source" ]; then
  provider_name=$(provider_display_name "$provider_id" "$provider_source")
fi
if [ -z "$provider_name" ]; then
  provider_name=$provider_id
fi

if [ "$had_config" -eq 1 ]; then
  LC_ALL=C awk -v target="model_providers.$provider_id" '
    function root_key(line, equals_at, key, first, last, quote) {
      equals_at = index(line, "=")
      if (equals_at == 0) return ""
      key = substr(line, 1, equals_at - 1)
      sub(/^[[:space:]]+/, "", key)
      sub(/[[:space:]]+$/, "", key)
      if (length(key) >= 2) {
        first = substr(key, 1, 1)
        last = substr(key, length(key), 1)
        quote = sprintf("%c", 39)
        if ((first == "\"" && last == "\"") || (first == quote && last == quote)) {
          key = substr(key, 2, length(key) - 2)
        }
      }
      return key
    }
    BEGIN { current = ""; skip = 0 }
    /^[[:space:]]*\[[^]]+\][[:space:]]*(#.*)?$/ {
      section = $0
      sub(/^[[:space:]]*\[/, "", section)
      sub(/\].*$/, "", section)
      current = section
      skip = (current == "model_providers.madapi" || index(current, "model_providers.madapi.") == 1 || current == target || index(current, target ".") == 1)
      if (skip) next
    }
    skip { next }
    current == "" {
      key = root_key($0)
      if (key == "model_provider" || key == "model" || key == "model_catalog_json") next
    }
    { print }
  ' "$config_path" > "$body_path"
else
  : > "$body_path"
fi

cleanup() {
  if [ "$success" -ne 1 ]; then
    if [ "$config_installed" -eq 1 ]; then
      if [ "$had_config" -eq 1 ] && [ -n "$backup_path" ] && [ -f "$backup_path" ]; then
        cp "$backup_path" "$config_path"
      elif [ "$had_config" -eq 0 ]; then
        rm -f "$config_path"
      fi
    fi
    if [ "$key_installed" -eq 1 ]; then
      rm -f "$key_path"
    fi
    if [ "$had_key" -eq 1 ] && [ -f "$rollback_key_path" ]; then
      mv "$rollback_key_path" "$key_path"
    fi
  fi
  rm -f "$temp_path" "$body_path" "$temp_key_path" "$rollback_key_path"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

{
  printf 'model_provider = %s\n' "$(toml_string "$provider_id")"
  printf '%s\n' 'model = "gpt-5.6-sol"'
  [ "$has_reasoning" -eq 1 ] || printf '%s\n' 'model_reasoning_effort = "high"'
  [ "$has_compact" -eq 1 ] || printf '%s\n' 'model_auto_compact_token_limit = 500000'
  printf '\n'
  cat "$body_path"
  printf '\n[model_providers.%s]\n' "$provider_id"
  printf 'name = %s\n' "$(toml_string "$provider_name")"
  printf '%s\n' 'base_url = "https://mad.myddns.me/codex/v1"'
  printf '%s\n' 'wire_api = "responses"'
  printf '%s\n' 'stream_idle_timeout_ms = 360000'
  printf '%s\n' 'request_max_retries = 3'
  printf '%s\n\n' 'context_window_override = 1048576'
  printf '[model_providers.%s.auth]\n' "$provider_id"
  printf '%s\n' 'command = "/bin/sh"'
  printf '%s\n' 'args = ["-c", "h=${CODEX_HOME:-$HOME/.codex}; exec cat \"$h/madapi.key\""]'
  printf '%s\n' 'timeout_ms = 5000'
  printf '%s\n' 'refresh_interval_ms = 300000'
} > "$temp_path"

printf '%s' "$api_key" > "$temp_key_path"
chmod 600 "$temp_key_path"

if [ "$had_config" -eq 1 ]; then
  backup_path="$config_path.madapi-backup-$(date '+%Y%m%d-%H%M%S')-$$"
  cp -p "$config_path" "$backup_path"
  chmod 600 "$backup_path"
fi
if [ "$had_key" -eq 1 ]; then
  mv "$key_path" "$rollback_key_path"
fi
mv "$temp_key_path" "$key_path"
key_installed=1
mv "$temp_path" "$config_path"
config_installed=1
chmod 600 "$config_path" "$key_path"

if ! codex_config_valid; then
  printf '%s\n' 'The generated MadAPI configuration could not be parsed by Codex.' >&2
  exit 1
fi

rm -f "$models_cache_path" "$rollback_key_path"
success=1

printf 'MadAPI Codex configuration installed: %s\n' "$config_path"
if [ -n "$backup_path" ]; then
  printf 'Backup created: %s\n' "$backup_path"
fi
printf '%s\n' 'Restart Codex to load the model catalog.'
