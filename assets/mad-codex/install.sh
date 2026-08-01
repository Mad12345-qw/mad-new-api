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
transaction_id="$$-$(date '+%s')"
temp_path="$codex_home/config.toml.madapi.$transaction_id.tmp"
body_path="$codex_home/config.toml.madapi.$transaction_id.body"
backup_path=
had_config=0
config_installed=0
success=0

umask 077
mkdir -p "$codex_home"
[ -f "$config_path" ] && had_config=1

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
  file=${2:-$config_path}
  awk -v key="$key" '
    BEGIN { found = 0 }
    /^[[:space:]]*\[/ { exit }
    $0 ~ "^[[:space:]]*" key "[[:space:]]*=" { found = 1 }
    END { exit(found ? 0 : 1) }
  ' "$file"
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

provider_has_assignment() {
  provider=$1
  key=$2
  expected=$3
  file=$4
  awk -v target="model_providers.$provider" -v key="$key" -v expected="$expected" '
    /^[[:space:]]*\[[^]]+\][[:space:]]*(#.*)?$/ {
      section = $0
      sub(/^[[:space:]]*\[/, "", section)
      sub(/\].*$/, "", section)
      current = section
      next
    }
    current == target && $0 ~ "^[[:space:]]*" key "[[:space:]]*=" {
      value = $0
      sub(/^[^=]*=[[:space:]]*/, "", value)
      sub(/[[:space:]]*(#.*)?$/, "", value)
      exit(value == expected ? 0 : 1)
    }
    END { if (current != target) exit 1 }
  ' "$file"
}

toml_string() {
  printf '%s' "$1" | awk '{ gsub(/\\/, "\\\\"); gsub(/"/, "\\\""); printf "\"%s\"", $0 }'
}

has_reasoning=0
has_compact=0
provider_source=$config_path
provider_id=
legacy_injected_storage=0
if [ "$had_config" -eq 1 ]; then
  provider_id=$(root_string_value model_provider "$config_path")
  if [ -n "$provider_id" ] &&
     has_root_key disable_response_storage "$config_path" &&
     provider_has_assignment "$provider_id" base_url '"https://mad.myddns.me/codex/v1"' "$config_path" &&
     provider_has_assignment "$provider_id" request_max_retries '0' "$config_path"; then
    for recovery_path in "$config_path".madapi-backup-*; do
      [ -f "$recovery_path" ] || continue
      if ! has_root_key disable_response_storage "$recovery_path"; then
        legacy_injected_storage=1
        break
      fi
    done
  fi
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
    current == "" && legacy_storage == 1 && /^[[:space:]]*disable_response_storage[[:space:]]*=/ { next }
    current == "" && /^[[:space:]]*(model_provider|model|model_catalog_json)[[:space:]]*=/ { next }
    { print }
  ' legacy_storage="$legacy_injected_storage" "$config_path" > "$body_path"
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
  fi
  rm -f "$temp_path" "$body_path"
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
  printf '%s\n' 'requires_openai_auth = true'
  printf 'experimental_bearer_token = %s\n' "$(toml_string "$api_key")"
  printf '%s\n' 'stream_idle_timeout_ms = 360000'
  printf '%s\n' 'request_max_retries = 3'
  printf '%s\n' 'context_window_override = 1048576'
} > "$temp_path"

if [ "$had_config" -eq 1 ]; then
  backup_path="$config_path.madapi-backup-$(date '+%Y%m%d-%H%M%S')-$$"
  cp -p "$config_path" "$backup_path"
  chmod 600 "$backup_path"
fi
mv "$temp_path" "$config_path"
config_installed=1
chmod 600 "$config_path"

if ! codex_config_valid; then
  printf '%s\n' 'The generated MadAPI configuration could not be parsed by Codex.' >&2
  exit 1
fi

success=1

printf 'MadAPI Codex configuration installed: %s\n' "$config_path"
if [ -n "$backup_path" ]; then
  printf 'Backup created: %s\n' "$backup_path"
fi
printf '%s\n' 'Restart Codex to load the model catalog.'
