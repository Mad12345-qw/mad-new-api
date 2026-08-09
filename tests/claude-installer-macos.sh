#!/bin/sh
set -eu

installer=$1
image_tool=$2
root=$(mktemp -d "${TMPDIR:-/tmp}/madapi-claude-test.XXXXXX")
trap 'rm -rf "$root"' EXIT HUP INT TERM
normal="$root/normal"
threep="$root/threep"
tool="$root/tool"
library="$threep/configLibrary"
mkdir -p "$normal" "$library" "$tool/cache"
printf '\001\002\003\004' > "$tool/cache/keep-image.png"

printf '%s' '{"oauthAccount":"keep-account","historyMarker":"keep-history","mcpServers":{"existing":{"command":"keep"}}}' > "$normal/claude_desktop_config.json"
printf '%s' '{"customSetting":"keep-setting"}' > "$threep/claude_desktop_config.json"
printf '%s' '{"appliedId":"existing-id","entries":[{"id":"existing-id","name":"Existing"}]}' > "$library/_meta.json"
printf '%s' '{"data":[{"id":"claude-fable-5"},{"id":"claude-opus-4-8"},{"id":"claude-opus-5"},{"id":"claude-sonnet-5"},{"id":"claude-haiku-4-5"}]}' > "$root/models.json"

run_installer() {
  MADAPI_KEY='sk-test-claude-installer' \
  MADAPI_INSTALL_TEST_MODE=1 \
  MADAPI_MODELS_FIXTURE_PATH="$root/models.json" \
  MADAPI_CLAUDE_NORMAL_DIR="$normal" \
  MADAPI_CLAUDE_THREEP_DIR="$threep" \
  MADAPI_CLAUDE_TOOL_DIR="$tool" \
  MADAPI_CLAUDE_IMAGE_SOURCE_DIR="$image_tool" \
  MADAPI_CLAUDE_FORCE_PORTABLE_NODE="${MADAPI_CLAUDE_FORCE_PORTABLE_NODE:-0}" \
  MADAPI_CLAUDE_NODE_RUNTIME_PATH="${MADAPI_CLAUDE_NODE_RUNTIME_PATH:-}" \
  MADAPI_CLAUDE_INSTALL_LANGUAGE="${MADAPI_CLAUDE_INSTALL_LANGUAGE:-0}" \
  MADAPI_CLAUDE_LANGUAGE_INSTALLER_PATH="${MADAPI_CLAUDE_LANGUAGE_INSTALLER_PATH:-}" \
  /bin/sh "$installer"
}

run_installer
run_installer

MADAPI_TEST_NORMAL="$normal/claude_desktop_config.json" \
MADAPI_TEST_THREEP="$threep/claude_desktop_config.json" \
MADAPI_TEST_META="$library/_meta.json" \
MADAPI_TEST_LIBRARY="$library" \
/usr/bin/osascript -l JavaScript <<'JXA'
ObjC.import('Foundation');
function env(name) { return ObjC.unwrap($.NSProcessInfo.processInfo.environment.objectForKey(name)); }
function read(file) {
  const text = ObjC.unwrap($.NSString.stringWithContentsOfFileEncodingError(file, $.NSUTF8StringEncoding, null));
  return JSON.parse(text);
}
function assert(value, message) { if (!value) throw new Error(message); }
const normal = read(env('MADAPI_TEST_NORMAL'));
const threep = read(env('MADAPI_TEST_THREEP'));
const meta = read(env('MADAPI_TEST_META'));
const gateway = read(env('MADAPI_TEST_LIBRARY') + '/' + meta.appliedId + '.json');
assert(normal.oauthAccount === 'keep-account', 'OAuth account data was not preserved');
assert(normal.historyMarker === 'keep-history', 'History marker was not preserved');
assert(normal.mcpServers.existing.command === 'keep', 'Existing MCP server was not preserved');
assert(typeof normal.mcpServers['madapi-image'].command === 'string' && normal.mcpServers['madapi-image'].command.length > 0, 'Image MCP command is missing');
assert(normal.deploymentMode === '3p' && threep.deploymentMode === '3p', 'Gateway mode is missing');
assert(threep.customSetting === 'keep-setting', '3p custom setting was not preserved');
const expected = ['claude-fable-5','claude-opus-4-8','claude-opus-5','claude-sonnet-5','claude-haiku-4-5'];
assert(JSON.stringify(gateway.inferenceModels.map((item) => item.name)) === JSON.stringify(expected), 'Gateway models are incorrect');
assert(meta.entries.filter((entry) => entry.name === 'MadAPI').length === 1, 'Duplicate MadAPI entries found');
assert(meta.entries.filter((entry) => entry.name === 'Existing').length === 1, 'Existing entry was lost');
JXA

[ -f "$tool/server.mjs" ]
[ -f "$tool/widget.html" ]
[ -f "$tool/cache/keep-image.png" ]

printf '%s\n' '#!/bin/sh' 'exit 9' > "$root/language-failure.sh"
chmod 700 "$root/language-failure.sh"
MADAPI_CLAUDE_INSTALL_LANGUAGE=1 \
MADAPI_CLAUDE_LANGUAGE_INSTALLER_PATH="$root/language-failure.sh" \
run_installer
[ -f "$tool/server.mjs" ]
[ -f "$tool/cache/keep-image.png" ]

MADAPI_CLAUDE_FORCE_PORTABLE_NODE=1 \
MADAPI_CLAUDE_NODE_RUNTIME_PATH="$(command -v node)" \
run_installer
[ -x "$tool/runtime/node" ]
MADAPI_TEST_NORMAL="$normal/claude_desktop_config.json" \
MADAPI_EXPECTED_NODE="$tool/runtime/node" \
/usr/bin/osascript -l JavaScript <<'JXA'
ObjC.import('Foundation');
function env(name) { return ObjC.unwrap($.NSProcessInfo.processInfo.environment.objectForKey(name)); }
const text = ObjC.unwrap($.NSString.stringWithContentsOfFileEncodingError(env('MADAPI_TEST_NORMAL'), $.NSUTF8StringEncoding, null));
if (JSON.parse(text).mcpServers['madapi-image'].command !== env('MADAPI_EXPECTED_NODE')) throw new Error('Portable Node command is incorrect');
JXA

normal_hash=$(shasum -a 256 "$normal/claude_desktop_config.json" | awk '{print $1}')
threep_hash=$(shasum -a 256 "$threep/claude_desktop_config.json" | awk '{print $1}')
meta_hash=$(shasum -a 256 "$library/_meta.json" | awk '{print $1}')
printf '%s' '{"data":[{"id":"claude-fable-5"}]}' > "$root/models.json"
if run_installer >/dev/null 2>&1; then
  printf '%s\n' 'Missing model validation did not fail.' >&2
  exit 1
fi
[ "$(shasum -a 256 "$normal/claude_desktop_config.json" | awk '{print $1}')" = "$normal_hash" ]
[ "$(shasum -a 256 "$threep/claude_desktop_config.json" | awk '{print $1}')" = "$threep_hash" ]
[ "$(shasum -a 256 "$library/_meta.json" | awk '{print $1}')" = "$meta_hash" ]
[ -f "$tool/cache/keep-image.png" ]

printf '%s' '{"data":[{"id":"claude-fable-5"},{"id":"claude-opus-4-8"},{"id":"claude-opus-5"},{"id":"claude-sonnet-5"},{"id":"claude-haiku-4-5"}]}' > "$root/models.json"
if MADAPI_FORCE_POSTWRITE_FAILURE=1 run_installer >/dev/null 2>&1; then
  printf '%s\n' 'Forced post-write failure did not fail.' >&2
  exit 1
fi
[ "$(shasum -a 256 "$normal/claude_desktop_config.json" | awk '{print $1}')" = "$normal_hash" ]
[ "$(shasum -a 256 "$threep/claude_desktop_config.json" | awk '{print $1}')" = "$threep_hash" ]
[ "$(shasum -a 256 "$library/_meta.json" | awk '{print $1}')" = "$meta_hash" ]

printf '%s\n' 'Claude macOS installer acceptance passed.'
