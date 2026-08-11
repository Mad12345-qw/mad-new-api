#!/bin/sh
set -eu

api_key=${MADAPI_KEY:-}
case "$api_key" in
  sk-*[!A-Za-z0-9._-]*|'') printf '%s\n' 'MADAPI_KEY is missing or invalid.' >&2; exit 1 ;;
  sk-*) ;;
  *) printf '%s\n' 'MADAPI_KEY is missing or invalid.' >&2; exit 1 ;;
esac
gateway_key_env_name=MADAPI_API_KEY
madapi_base_url=${MADAPI_BASE_URL:-https://mad.myddns.me}
madapi_base_url=${madapi_base_url%/}
case "$madapi_base_url" in http://*|https://*) ;; *) printf '%s\n' 'MADAPI_BASE_URL is invalid.' >&2; exit 1 ;; esac
codex_base_url=$madapi_base_url/codex/v1
requested_login_mode=${MADAPI_CODEX_LOGIN_MODE:-auto}
case "$requested_login_mode" in auto|oauth|apikey) ;; *) printf '%s\n' 'MADAPI_CODEX_LOGIN_MODE must be auto, oauth, or apikey.' >&2; exit 1 ;; esac

codex_home=${CODEX_HOME:-"$HOME/.codex"}
config_path="$codex_home/config.toml"
auth_path="$codex_home/auth.json"
models_cache_path="$codex_home/models_cache.json"
catalog_path="$codex_home/madapi-cockpit-model-catalog.json"
refresh_script_path="$codex_home/madapi-refresh-model-catalog.sh"
history_script_path="$codex_home/madapi-restore-history.sh"
transaction_id="$$-$(date '+%s')"
temp_path="$codex_home/config.toml.madapi.$transaction_id.tmp"
temp_refresh_path="$codex_home/madapi-refresh-model-catalog.$transaction_id.tmp"
temp_history_path="$codex_home/madapi-restore-history.$transaction_id.tmp"
temp_catalog_path="$codex_home/madapi-cockpit-model-catalog.$transaction_id.tmp"
temp_auth_path="$codex_home/auth.json.madapi.$transaction_id.tmp"
temp_plist_path="$codex_home/madapi-model-catalog.$transaction_id.plist"
body_path="$codex_home/config.toml.madapi.$transaction_id.body"
backup_path=
auth_backup_path=
history_backup_path=
had_config=0
had_auth=0
config_installed=0
auth_changed=0
success=0
test_mode=${MADAPI_INSTALL_TEST_MODE:-0}
staging_home=

if [ "$test_mode" != 1 ]; then
  /usr/bin/osascript -e 'tell application "Codex" to quit' >/dev/null 2>&1 || true
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    pgrep -x Codex >/dev/null 2>&1 || break
    sleep 1
  done
  if pgrep -x Codex >/dev/null 2>&1; then
    printf '%s\n' 'Codex Desktop did not exit. Close Codex completely, then run the command again.' >&2
    exit 1
  fi
fi

umask 077
auth_kind=unconfigured
if [ -f "$auth_path" ]; then
  had_auth=1
  /usr/bin/plutil -convert json -o - "$auth_path" >/dev/null 2>&1 || { printf '%s\n' 'Codex Desktop authentication state is unreadable. No files were changed.' >&2; exit 1; }
  existing_mode=$(/usr/bin/plutil -extract auth_mode raw -o - "$auth_path" 2>/dev/null || true)
  existing_api_key=$(/usr/bin/plutil -extract OPENAI_API_KEY raw -o - "$auth_path" 2>/dev/null || true)
  existing_access_token=$(/usr/bin/plutil -extract tokens.access_token raw -o - "$auth_path" 2>/dev/null || true)
  existing_refresh_token=$(/usr/bin/plutil -extract tokens.refresh_token raw -o - "$auth_path" 2>/dev/null || true)
  if [ "$existing_mode" != 'apikey' ] && [ -n "$existing_access_token" ] && [ "$existing_access_token" != 'null' ] && [ -n "$existing_refresh_token" ] && [ "$existing_refresh_token" != 'null' ]; then
    auth_kind=oauth
  elif [ "$existing_mode" = 'apikey' ] || { [ -n "$existing_api_key" ] && [ "$existing_api_key" != 'null' ]; }; then
    auth_kind=apikey
  fi
fi
existing_auth_kind=$auth_kind
auth_mutation=none
if [ "$requested_login_mode" = oauth ]; then
  auth_kind=oauth
elif [ "$requested_login_mode" = apikey ]; then
  auth_kind=apikey
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
    BEGIN { current = ""; skip = 0; features = 0 }
    /^[[:space:]]*\[[^]]+\][[:space:]]*(#.*)?$/ {
      section = $0; sub(/^[[:space:]]*\[/, "", section); sub(/\].*$/, "", section); current = section
      skip = (current == "model_providers.madapi" || index(current, "model_providers.madapi.") == 1 || current == target || index(current, target ".") == 1)
      if (skip) next
      if (current == "features") { features = 1; print; print "image_generation = true"; next }
    }
    skip { next }
    current == "features" && /^[[:space:]]*image_generation[[:space:]]*=/ { next }
    current == "" { key = root_key($0); if (key == "model_provider" || key == "model_catalog_json") next }
    { print }
    END { if (!features) { print ""; print "[features]"; print "image_generation = true" } }
  ' "$config_path" > "$body_path"
else
  : > "$body_path"
fi

cleanup() {
  if [ "$success" -ne 1 ] && [ -n "$history_backup_path" ] && [ -d "$history_backup_path" ]; then
    for name in session_index.jsonl .codex-global-state.json state_5.sqlite state_5.sqlite-wal state_5.sqlite-shm; do
      if [ -f "$history_backup_path/$name" ]; then cp -p "$history_backup_path/$name" "$codex_home/$name"; else rm -f "$codex_home/$name"; fi
    done
  fi
  if [ "$success" -ne 1 ] && [ "$auth_changed" -eq 1 ]; then
    if [ "$had_auth" -eq 1 ] && [ -n "$auth_backup_path" ] && [ -f "$auth_backup_path" ]; then cp "$auth_backup_path" "$auth_path"; else rm -f "$auth_path"; fi
  fi
  if [ "$success" -ne 1 ] && [ "$config_installed" -eq 1 ]; then
    if [ "$had_config" -eq 1 ] && [ -n "$backup_path" ] && [ -f "$backup_path" ]; then cp "$backup_path" "$config_path"; else rm -f "$config_path"; fi
  fi
  rm -f "$temp_path" "$temp_refresh_path" "$temp_history_path" "$temp_catalog_path" "$temp_auth_path" "$temp_plist_path" "$body_path"
  [ -z "$staging_home" ] || rm -rf "$staging_home"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

{
  printf 'model_provider = %s\n' "$(toml_string "$provider_id")"
  printf '%s\n' 'model_catalog_json = "madapi-cockpit-model-catalog.json"'
  if [ "$had_config" -eq 0 ]; then
    printf '%s\n' 'model = "gpt-5.6-sol"'
    printf '%s\n' 'model_reasoning_effort = "high"'
    printf '%s\n' 'model_auto_compact_token_limit = 500000'
  fi
  printf '\n'
  cat "$body_path"
  printf '\n[model_providers.%s]\n' "$provider_id"
  printf 'name = %s\n' "$(toml_string "$provider_name")"
  printf 'base_url = %s\n' "$(toml_string "$codex_base_url")"
  printf '%s\n' 'wire_api = "responses"'
  printf '%s\n' 'requires_openai_auth = false'
  printf 'env_key = %s\n' "$(toml_string "$gateway_key_env_name")"
  printf '%s\n' 'http_headers = { "x-openai-actor-authorization" = "madapi-gateway" }'
  printf '%s\n' 'supports_websockets = false'
  printf '%s\n' 'stream_idle_timeout_ms = 360000'
  printf '%s\n' 'request_max_retries = 3'
  printf '%s\n' 'context_window_override = 1048576'
} > "$temp_path"

refresh_source=${MADAPI_REFRESH_SCRIPT_SOURCE:-}
if [ -z "$refresh_source" ]; then
  script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd || true)
  if [ -n "$script_dir" ] && [ -f "$script_dir/refresh-model-catalog.sh" ]; then
    refresh_source="$script_dir/refresh-model-catalog.sh"
  fi
fi
if [ -n "$refresh_source" ]; then
  cp "$refresh_source" "$temp_refresh_path"
elif [ "$test_mode" = 1 ]; then
  printf '%s\n' 'MADAPI_REFRESH_SCRIPT_SOURCE is required in installer test mode.' >&2
  exit 1
else
  curl -fsSL "$madapi_base_url/mad-codex/refresh-model-catalog.sh" -o "$temp_refresh_path"
fi
[ -s "$temp_refresh_path" ] || { printf '%s\n' 'The MadAPI model catalog refresh script is invalid.' >&2; exit 1; }

history_source=${MADAPI_HISTORY_RESTORE_SCRIPT_SOURCE:-}
if [ -z "$history_source" ]; then
  script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd || true)
  if [ -n "$script_dir" ] && [ -f "$script_dir/restore-history.sh" ]; then
    history_source="$script_dir/restore-history.sh"
  fi
fi
if [ -n "$history_source" ]; then
  cp "$history_source" "$temp_history_path"
elif [ "$test_mode" = 1 ]; then
  printf '%s\n' 'MADAPI_HISTORY_RESTORE_SCRIPT_SOURCE is required in installer test mode.' >&2
  exit 1
else
  curl -fsSL "$madapi_base_url/mad-codex/restore-history.sh" -o "$temp_history_path"
fi
[ -s "$temp_history_path" ] || { printf '%s\n' 'The MadAPI history restore script is invalid.' >&2; exit 1; }

if [ "$test_mode" = 1 ]; then
  printf '%s' '{"models":[{"slug":"gpt-5.6-sol","display_name":"gpt-5.6-sol"}]}' > "$temp_catalog_path"
else
  staging_home="$codex_home/madapi-catalog-stage-$transaction_id"
  mkdir -p "$staging_home"
  cp "$temp_path" "$staging_home/config.toml"
  if ! MADAPI_API_KEY="$api_key" MADAPI_BASE_URL="$madapi_base_url" CODEX_HOME="$staging_home" /bin/sh "$temp_refresh_path"; then
    rm -rf "$staging_home"
    printf '%s\n' 'Unable to download the initial MadAPI model catalog.' >&2
    exit 1
  fi
  mv "$staging_home/madapi-cockpit-model-catalog.json" "$temp_catalog_path"
  rm -rf "$staging_home"
  staging_home=
fi

if [ "$had_config" -eq 1 ]; then
  backup_path="$config_path.madapi-backup-$(date '+%Y%m%d-%H%M%S')-$$"
  cp -p "$config_path" "$backup_path"
fi
mv "$temp_path" "$config_path"
config_installed=1
chmod 600 "$config_path"
mv "$temp_refresh_path" "$refresh_script_path"
chmod 700 "$refresh_script_path"
mv "$temp_history_path" "$history_script_path"
chmod 700 "$history_script_path"
mv "$temp_catalog_path" "$catalog_path"
chmod 600 "$catalog_path"
if [ "$test_mode" != 1 ]; then
    launch_agents="$HOME/Library/LaunchAgents"
    plist_path="$launch_agents/me.madapi.codex-model-catalog.plist"
    escaped_script=$(printf '%s' "$refresh_script_path" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g')
    cat > "$temp_plist_path" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>me.madapi.codex-model-catalog</string>
    <key>ProgramArguments</key><array><string>/bin/sh</string><string>$escaped_script</string></array>
    <key>EnvironmentVariables</key><dict><key>$gateway_key_env_name</key><string>$api_key</string><key>MADAPI_BASE_URL</key><string>$madapi_base_url</string></dict>
<key>RunAtLoad</key><true/>
<key>StandardOutPath</key><string>/dev/null</string>
<key>StandardErrorPath</key><string>/dev/null</string>
</dict></plist>
EOF
    /usr/bin/plutil -lint "$temp_plist_path" >/dev/null
    mkdir -p "$launch_agents"
    mv "$temp_plist_path" "$plist_path"
    chmod 600 "$plist_path"
    uid=$(/usr/bin/id -u)
    /bin/launchctl bootout "gui/$uid/me.madapi.codex-model-catalog" >/dev/null 2>&1 || true
    /bin/launchctl bootstrap "gui/$uid" "$plist_path"
    /bin/launchctl setenv "$gateway_key_env_name" "$api_key"
fi
rm -f "$models_cache_path"
history_backup_path="$codex_home/madapi-install-history-backup-$transaction_id"
MADAPI_HISTORY_PROVIDER="$provider_id" MADAPI_HISTORY_BACKUP_DIR="$history_backup_path" /bin/sh "$history_script_path"
success=1

printf 'MadAPI Codex desktop configuration installed: %s\n' "$config_path"
[ -z "$backup_path" ] || printf 'Backup created: %s\n' "$backup_path"
[ -z "$auth_backup_path" ] || printf 'Authentication backup created: %s\n' "$auth_backup_path"
[ -z "$history_backup_path" ] || printf 'History backup created: %s\n' "$history_backup_path"
if [ "$requested_login_mode" = oauth ] && [ "$existing_auth_kind" != oauth ]; then
  printf '%s\n' 'MadAPI installed. Sign in with ChatGPT after Codex restarts to keep an OAuth account connected.'
elif [ "$requested_login_mode" = apikey ]; then
  printf '%s\n' 'MadAPI API Key mode installed without changing Codex account state.'
elif [ "$auth_kind" = 'oauth' ]; then
  printf '%s\n' 'Existing ChatGPT OAuth session preserved.'
elif [ "$auth_kind" = 'apikey' ]; then
  printf '%s\n' 'Existing Codex Desktop API Key sign-in preserved.'
else
  printf '%s\n' 'Codex Desktop sign-in was not changed. Choose ChatGPT OAuth or API Key when Codex opens.'
fi
printf '%s\n' 'Restart Codex Desktop to refresh the model list.'
