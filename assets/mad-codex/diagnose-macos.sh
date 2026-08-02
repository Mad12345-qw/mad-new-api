#!/bin/sh
set -eu

codex_app=${CODEX_APP_SERVER_PATH:-'/Applications/ChatGPT.app/Contents/Resources/codex'}
codex_home=${CODEX_HOME:-"$HOME/.codex"}
config_path="$codex_home/config.toml"
timestamp=$(date '+%Y%m%d-%H%M%S')
report_dir="$HOME/Desktop/Codex-app-server-check-$timestamp"
archive_path="$report_dir.zip"
sandbox_dir=$(mktemp -d "${TMPDIR:-/tmp}/madapi-codex-check.XXXXXX")

cleanup() {
  rm -rf "$sandbox_dir"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

if [ ! -x "$codex_app" ]; then
  printf '%s\n' "Codex Desktop app-server was not found: $codex_app" >&2
  exit 1
fi
if [ ! -f "$config_path" ]; then
  printf '%s\n' "Codex Desktop config was not found: $config_path" >&2
  exit 1
fi

mkdir -p "$report_dir" "$sandbox_dir"
cp "$config_path" "$sandbox_dir/config.toml"
if [ -f "$codex_home/auth.json" ]; then
  cp "$codex_home/auth.json" "$sandbox_dir/auth.json"
fi

awk '
  /^[[:space:]]*experimental_bearer_token[[:space:]]*=/ {
    print "experimental_bearer_token = \"<redacted>\""
    next
  }
  { print }
' "$config_path" > "$report_dir/config-redacted.toml"

{
  printf 'Collected at: %s\n' "$(date '+%Y-%m-%d %H:%M:%S %z')"
  printf 'CODEX_HOME: %s\n' "$codex_home"
  printf 'App server: %s\n' "$codex_app"
  printf 'App server version: '
  "$codex_app" --version 2>&1 || true
  printf 'macOS: '
  sw_vers -productVersion 2>/dev/null || true
  printf '\nExisting model cache files before isolated check:\n'
  for search_root in "$codex_home" "$HOME/Library/Application Support" "$HOME/Library/Caches"; do
    if [ -d "$search_root" ]; then
      find "$search_root" -type f -name models_cache.json -print 2>/dev/null || true
    fi
  done
} > "$report_dir/environment.txt"

{
  printf '%s\n' '{"id":1,"method":"initialize","params":{"clientInfo":{"name":"madapi-model-diagnostic","version":"1.0.0"},"capabilities":{"experimentalApi":true}}}'
  printf '%s\n' '{"method":"initialized"}'
  printf '%s\n' '{"id":2,"method":"model/list","params":{"limit":100,"includeHidden":true}}'
} | CODEX_HOME="$sandbox_dir" "$codex_app" app-server --stdio \
  > "$report_dir/app-server-stdout.jsonl" \
  2> "$report_dir/app-server-stderr.log" || true

if [ -f "$sandbox_dir/models_cache.json" ]; then
  cp "$sandbox_dir/models_cache.json" "$report_dir/models_cache.json"
  printf '%s\n' 'Isolated app-server created models_cache.json.' >> "$report_dir/environment.txt"
else
  printf '%s\n' 'Isolated app-server did not create models_cache.json.' >> "$report_dir/environment.txt"
fi

ditto -c -k --sequesterRsrc --keepParent "$report_dir" "$archive_path"
printf '%s\n' "Diagnostic archive created: $archive_path"
printf '%s\n' 'The existing config, sessions, login state, and model cache were not changed.'
