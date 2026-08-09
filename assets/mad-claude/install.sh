#!/bin/sh
set -eu

api_key=${MADAPI_KEY:-}
case "$api_key" in
  sk-*[!A-Za-z0-9._-]*|'') printf '%s\n' 'MADAPI_KEY is missing or invalid.' >&2; exit 1 ;;
  sk-*) ;;
  *) printf '%s\n' 'MADAPI_KEY is missing or invalid.' >&2; exit 1 ;;
esac

base_url=${MADAPI_CLAUDE_BASE_URL:-https://mad.myddns.me/v1}
base_url=${base_url%/}
test_mode=${MADAPI_INSTALL_TEST_MODE:-0}
models='claude-fable-5 claude-opus-4-8 claude-opus-5 claude-sonnet-5 claude-haiku-4-5'

stage_root=$(mktemp -d "${TMPDIR:-/tmp}/madapi-claude.XXXXXX")
models_path="$stage_root/models.json"
stage_tool="$stage_root/image-tool"
mkdir -p "$stage_tool"

cleanup_stage() {
  rm -rf "$stage_root"
}
trap cleanup_stage EXIT HUP INT TERM

printf '%s\n' 'Checking MadAPI model access...'
if [ -n "${MADAPI_MODELS_FIXTURE_PATH:-}" ]; then
  cp "$MADAPI_MODELS_FIXTURE_PATH" "$models_path"
else
  curl -fsS --max-time 30 \
    -H "x-api-key: $api_key" \
    -H 'Accept: application/json' \
    "$base_url/models" -o "$models_path"
fi

MADAPI_MODELS_PATH="$models_path" MADAPI_EXPECTED_MODELS="$models" /usr/bin/osascript -l JavaScript <<'JXA'
ObjC.import('Foundation');
function env(name) {
  const value = $.NSProcessInfo.processInfo.environment.objectForKey(name);
  return value ? ObjC.unwrap(value) : '';
}
const text = ObjC.unwrap($.NSString.stringWithContentsOfFileEncodingError(env('MADAPI_MODELS_PATH'), $.NSUTF8StringEncoding, null));
const payload = JSON.parse(text);
const available = new Set((payload.data || []).map((item) => String(item.id || '')));
const missing = env('MADAPI_EXPECTED_MODELS').split(' ').filter((model) => !available.has(model));
if (missing.length) throw new Error('The API key cannot access: ' + missing.join(', '));
JXA
printf '%s\n' 'All five Claude models are available.'

normal_root=${MADAPI_CLAUDE_NORMAL_DIR:-"$HOME/Library/Application Support/Claude"}
threep_root=${MADAPI_CLAUDE_THREEP_DIR:-"$HOME/Library/Application Support/Claude-3p"}
tool_root=${MADAPI_CLAUDE_TOOL_DIR:-"$HOME/Library/Application Support/MadAPI/claude-image-tool"}
normal_config="$normal_root/claude_desktop_config.json"
threep_config="$threep_root/claude_desktop_config.json"
library_dir="$threep_root/configLibrary"
meta_path="$library_dir/_meta.json"
config_id=$(/usr/bin/uuidgen | tr '[:upper:]' '[:lower:]')
gateway_path="$library_dir/$config_id.json"

mkdir -p "$normal_root" "$threep_root" "$library_dir" "$(dirname "$tool_root")"
backup_root="$normal_root/madapi-claude-backup-$(date '+%Y%m%d-%H%M%S')-$(/usr/bin/uuidgen | cut -c1-8)"
mkdir -p "$backup_root"
had_normal=0
had_threep=0
had_meta=0
had_tool=0
[ ! -f "$normal_config" ] || { cp -p "$normal_config" "$backup_root/normal-config.json"; had_normal=1; }
[ ! -f "$threep_config" ] || { cp -p "$threep_config" "$backup_root/threep-config.json"; had_threep=1; }
[ ! -f "$meta_path" ] || { cp -p "$meta_path" "$backup_root/library-meta.json"; had_meta=1; }
[ ! -d "$tool_root" ] || { cp -R "$tool_root" "$backup_root/image-tool"; had_tool=1; }

if [ -n "${MADAPI_CLAUDE_IMAGE_SOURCE_DIR:-}" ]; then
  cp "$MADAPI_CLAUDE_IMAGE_SOURCE_DIR/server.mjs" "$stage_tool/server.mjs"
  cp "$MADAPI_CLAUDE_IMAGE_SOURCE_DIR/widget.html" "$stage_tool/widget.html"
else
  asset_url='https://mad.myddns.me/mad-claude/image-tool'
  curl -fsS --max-time 60 "$asset_url/server.mjs" -o "$stage_tool/server.mjs"
  curl -fsS --max-time 60 "$asset_url/widget.html" -o "$stage_tool/widget.html"
fi
[ "$(wc -c < "$stage_tool/server.mjs")" -ge 1000 ] || { printf '%s\n' 'Image tool server is incomplete.' >&2; exit 1; }
[ "$(wc -c < "$stage_tool/widget.html")" -ge 500 ] || { printf '%s\n' 'Image tool widget is incomplete.' >&2; exit 1; }

portable_node="$tool_root/runtime/node"
system_node=$(command -v node 2>/dev/null || true)
if [ "${MADAPI_CLAUDE_FORCE_PORTABLE_NODE:-0}" != 1 ] && [ -n "$system_node" ]; then
  node_command=$system_node
else
  node_command=$portable_node
  if [ -n "${MADAPI_CLAUDE_NODE_RUNTIME_PATH:-}" ]; then
    [ -f "$MADAPI_CLAUDE_NODE_RUNTIME_PATH" ] || { printf '%s\n' 'Configured portable Node runtime is missing.' >&2; exit 1; }
    cp "$MADAPI_CLAUDE_NODE_RUNTIME_PATH" "$stage_tool/node"
  elif [ ! -x "$portable_node" ]; then
    node_version=22.23.2
    case "$(uname -m)" in
      arm64|aarch64)
        node_platform=darwin-arm64
        node_sha256=61130f394c1630d211dd50aecc4353d379480f36d3ac913cd85dbba1aed585c6
        ;;
      x86_64|amd64)
        node_platform=darwin-x64
        node_sha256=58e99022c2ff89395576cc7fd4d98cea24bb68081475d5f88b801ee8729fb026
        ;;
      *) printf '%s\n' 'Unsupported macOS architecture for portable Node.' >&2; exit 1 ;;
    esac
    node_archive="$stage_root/node-v$node_version-$node_platform.tar.gz"
    curl -fsS --max-time 180 \
      "https://nodejs.org/dist/v$node_version/node-v$node_version-$node_platform.tar.gz" \
      -o "$node_archive"
    [ "$(shasum -a 256 "$node_archive" | awk '{print $1}')" = "$node_sha256" ] || {
      printf '%s\n' 'Portable Node archive checksum mismatch.' >&2
      exit 1
    }
    mkdir -p "$stage_root/node-runtime"
    tar -xzf "$node_archive" -C "$stage_root/node-runtime"
    downloaded_node=$(find "$stage_root/node-runtime" -type f -path '*/bin/node' -print -quit)
    [ -n "$downloaded_node" ] || { printf '%s\n' 'Portable Node runtime is incomplete.' >&2; exit 1; }
    cp "$downloaded_node" "$stage_tool/node"
  fi
fi

restore_file() {
  target=$1
  backup=$2
  had_original=$3
  if [ "$had_original" -eq 1 ]; then cp -p "$backup" "$target"; else rm -f "$target"; fi
}

rollback() {
  restore_file "$normal_config" "$backup_root/normal-config.json" "$had_normal"
  restore_file "$threep_config" "$backup_root/threep-config.json" "$had_threep"
  restore_file "$meta_path" "$backup_root/library-meta.json" "$had_meta"
  rm -f "$gateway_path"
  rm -rf "$tool_root"
  [ "$had_tool" -ne 1 ] || cp -R "$backup_root/image-tool" "$tool_root"
}

if [ "$test_mode" != 1 ]; then
  /usr/bin/osascript -e 'tell application "Claude" to quit' >/dev/null 2>&1 || true
  count=0
  while pgrep -x Claude >/dev/null 2>&1 && [ "$count" -lt 10 ]; do
    sleep 1
    count=$((count + 1))
  done
  if pgrep -x Claude >/dev/null 2>&1; then pkill -TERM -x Claude >/dev/null 2>&1 || true; sleep 2; fi
fi

mkdir -p "$tool_root"
cp "$stage_tool/server.mjs" "$tool_root/server.mjs"
cp "$stage_tool/widget.html" "$tool_root/widget.html"
chmod 600 "$tool_root/server.mjs" "$tool_root/widget.html"
if [ -f "$stage_tool/node" ]; then
  mkdir -p "$(dirname "$portable_node")"
  cp "$stage_tool/node" "$portable_node"
  chmod 700 "$portable_node"
fi

export MADAPI_NORMAL_CONFIG="$normal_config"
export MADAPI_THREEP_CONFIG="$threep_config"
export MADAPI_LIBRARY_META="$meta_path"
export MADAPI_GATEWAY_CONFIG="$gateway_path"
export MADAPI_CONFIG_ID="$config_id"
export MADAPI_SERVER_PATH="$tool_root/server.mjs"
export MADAPI_NODE_COMMAND="$node_command"
export MADAPI_CLAUDE_BASE_URL="$base_url"
export MADAPI_EXPECTED_MODELS="$models"

if ! /usr/bin/osascript -l JavaScript <<'JXA'
ObjC.import('Foundation');
function env(name) {
  const value = $.NSProcessInfo.processInfo.environment.objectForKey(name);
  return value ? ObjC.unwrap(value) : '';
}
function readJson(file) {
  const data = $.NSData.dataWithContentsOfFile(file);
  if (!data) return {};
  const text = ObjC.unwrap($.NSString.alloc.initWithDataEncoding(data, $.NSUTF8StringEncoding));
  return text.trim() ? JSON.parse(text) : {};
}
function writeJson(file, value) {
  const text = $.NSString.stringWithString(JSON.stringify(value));
  if (!text.writeToFileAtomicallyEncodingError(file, true, $.NSUTF8StringEncoding, null)) {
    throw new Error('Unable to write ' + file);
  }
  JSON.parse(ObjC.unwrap($.NSString.stringWithContentsOfFileEncodingError(file, $.NSUTF8StringEncoding, null)));
}
function mergeDesktop(file) {
  const value = readJson(file);
  value.deploymentMode = '3p';
  if (!value.mcpServers || typeof value.mcpServers !== 'object' || Array.isArray(value.mcpServers)) value.mcpServers = {};
  value.mcpServers['madapi-image'] = { command: env('MADAPI_NODE_COMMAND'), args: [env('MADAPI_SERVER_PATH')] };
  writeJson(file, value);
}
mergeDesktop(env('MADAPI_NORMAL_CONFIG'));
mergeDesktop(env('MADAPI_THREEP_CONFIG'));
const modelNames = env('MADAPI_EXPECTED_MODELS').split(' ');
const gateway = {
  coworkEgressAllowedHosts: ['*'],
  disableDeploymentModeChooser: true,
  inferenceProvider: 'gateway',
  inferenceGatewayBaseUrl: env('MADAPI_CLAUDE_BASE_URL'),
  inferenceGatewayApiKey: env('MADAPI_KEY'),
  inferenceGatewayAuthScheme: 'x-api-key',
  inferenceModels: modelNames.map((name) => ({ name }))
};
writeJson(env('MADAPI_GATEWAY_CONFIG'), gateway);
const meta = readJson(env('MADAPI_LIBRARY_META'));
const entries = Array.isArray(meta.entries) ? meta.entries.filter((entry) => entry && entry.name !== 'MadAPI') : [];
entries.push({ id: env('MADAPI_CONFIG_ID'), name: 'MadAPI' });
meta.appliedId = env('MADAPI_CONFIG_ID');
meta.entries = entries;
writeJson(env('MADAPI_LIBRARY_META'), meta);
if (env('MADAPI_FORCE_POSTWRITE_FAILURE') === '1') throw new Error('Forced post-write failure for rollback acceptance');
const verifiedMeta = readJson(env('MADAPI_LIBRARY_META'));
const verifiedGateway = readJson(env('MADAPI_GATEWAY_CONFIG'));
if (verifiedMeta.appliedId !== env('MADAPI_CONFIG_ID')) throw new Error('Gateway activation check failed');
if (modelNames.some((name) => !verifiedGateway.inferenceModels.some((entry) => entry.name === name))) throw new Error('Gateway model verification failed');
JXA
then
  rollback
  printf '%s\n' 'Configuration failed. Previous Claude configuration was restored.' >&2
  exit 1
fi

if [ "$test_mode" != 1 ] && [ "${MADAPI_CLAUDE_SKIP_LANGUAGE:-0}" != 1 ]; then
  language_installer=${MADAPI_CLAUDE_LANGUAGE_INSTALLER_PATH:-}
  if [ -z "$language_installer" ]; then
    language_installer="$stage_root/install-language.sh"
    curl -fsS --max-time 60 'https://mad.myddns.me/mad-claude/install-language.sh' -o "$language_installer"
  fi
  /bin/sh "$language_installer"
fi

printf '%s\n' 'Claude Desktop MadAPI setup completed.'
printf 'BACKUP=%s\n' "$backup_root"
if [ "$test_mode" != 1 ]; then
  /usr/bin/open -a Claude >/dev/null 2>&1 || printf '%s\n' 'Start Claude Desktop manually.'
fi
