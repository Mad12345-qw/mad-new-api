#!/bin/sh
set -eu

codex_home=${CODEX_HOME:-$HOME/.codex}
catalog_path=$codex_home/madapi-cockpit-model-catalog.json
models_cache_path=$codex_home/models_cache.json
auth_path=$codex_home/auth.json
api_key=${MADAPI_API_KEY:-${MADAPI_KEY:-}}
base_url=${MADAPI_BASE_URL:-https://mad.myddns.me}
base_url=${base_url%/}
[ -n "$api_key" ] || { printf '%s\n' 'MADAPI_API_KEY is missing.' >&2; exit 1; }

auth_kind=${MADAPI_CODEX_AUTH_KIND:-}
if [ -z "$auth_kind" ]; then
  auth_kind=oauth
  if [ -f "$auth_path" ]; then
    mode=$(/usr/bin/plutil -extract auth_mode raw -o - "$auth_path" 2>/dev/null || true)
    openai_key=$(/usr/bin/plutil -extract OPENAI_API_KEY raw -o - "$auth_path" 2>/dev/null || true)
    if [ "$mode" = apikey ] || { [ -n "$openai_key" ] && [ "$openai_key" != null ]; }; then auth_kind=apikey; fi
  fi
fi
case "$auth_kind" in oauth|apikey) ;; *) printf '%s\n' 'MADAPI_CODEX_AUTH_KIND is invalid.' >&2; exit 1 ;; esac

mkdir -p "$codex_home"
available_path=$codex_home/madapi-models.$$.json
template_path=$codex_home/madapi-codex-model-templates.$$.json
output_path=$codex_home/madapi-cockpit-model-catalog.$$.tmp
trap 'rm -f "$available_path" "$template_path" "$output_path"' EXIT

if [ -n "${MADAPI_REFRESH_RESPONSE_FILE:-}" ]; then
  cp "$MADAPI_REFRESH_RESPONSE_FILE" "$available_path"
else
  curl -fsS -H "Authorization: Bearer $api_key" "$base_url/v1/models" -o "$available_path"
fi
if [ -n "${MADAPI_CODEX_TEMPLATE_FILE:-}" ]; then
  cp "$MADAPI_CODEX_TEMPLATE_FILE" "$template_path"
else
  curl -fsS "$base_url/mad-codex/codex-model-templates.json" -o "$template_path"
fi

AVAILABLE_PATH="$available_path" TEMPLATE_PATH="$template_path" OUTPUT_PATH="$output_path" AUTH_KIND="$auth_kind" /usr/bin/osascript -l JavaScript <<'JXA'
ObjC.import('Foundation')
function readJson(name) {
  var path = ObjC.unwrap($.NSProcessInfo.processInfo.environment.objectForKey(name))
  var data = $.NSData.dataWithContentsOfFile(path)
  if (!data) throw new Error(name + ' is unreadable')
  var text = ObjC.unwrap($.NSString.alloc.initWithDataEncoding(data, $.NSUTF8StringEncoding))
  return JSON.parse(text)
}
var available = readJson('AVAILABLE_PATH')
var templatePayload = readJson('TEMPLATE_PATH')
var templates = Array.isArray(templatePayload.models) ? templatePayload.models : []
if (!templates.length) throw new Error('The Codex model catalog is empty')
var bySlug = {}
templates.forEach(function (item) {
  if (item && typeof item.slug === 'string') bySlug[item.slug.toLowerCase()] = item
})
var fallback = bySlug['gpt-5.5'] || templates[0]
var sourceModels = Array.isArray(available.data) ? available.data : (Array.isArray(available.models) ? available.models : [])
var authKind = ObjC.unwrap($.NSProcessInfo.processInfo.environment.objectForKey('AUTH_KIND'))
var apiIds = ['claude-fable-5','claude-opus-5','gpt-5.6-sol','gpt-5.6-terra','gpt-5.6-luna','grok-4.6','gpt-5.6-sol-pro','gpt-5.6-terra-pro']
var apiSlots = {
  'claude-fable-5': {shell: 'gpt-5.5', profile: 'gpt-5.5'},
  'claude-opus-5': {shell: 'gpt-5.4', profile: 'gpt-5.4'},
  'gpt-5.6-sol': {shell: 'gpt-5.6-sol', profile: 'gpt-5.6-sol'},
  'gpt-5.6-terra': {shell: 'gpt-5.6-terra', profile: 'gpt-5.6-terra'},
  'gpt-5.6-luna': {shell: 'gpt-5.6-luna', profile: 'gpt-5.6-luna'},
  'grok-4.6': {shell: 'gpt-5.4-mini', profile: 'gpt-5.4-mini'},
  'gpt-5.6-sol-pro': {shell: 'gpt-5.3-codex', profile: 'gpt-5.6-sol'},
  'gpt-5.6-terra-pro': {shell: 'gpt-5.2', profile: 'gpt-5.6-terra'}
}
if (authKind === 'apikey') {
  var availableById = {}
  sourceModels.forEach(function (item) {
    var id = item && typeof item.id === 'string' ? item.id : (item && typeof item.slug === 'string' ? item.slug : '')
    if (id) availableById[id.toLowerCase()] = item
  })
  var missing = apiIds.filter(function (id) { return !availableById[id] })
  if (missing.length) throw new Error('MadAPI API catalog is missing required models: ' + missing.join(', '))
  sourceModels = apiIds.map(function (id) { return availableById[id] })
}
var seen = {}
var result = []
sourceModels.forEach(function (item) {
  var id = item && typeof item.id === 'string' ? item.id : (item && typeof item.slug === 'string' ? item.slug : '')
  id = id.trim()
  var lower = id.toLowerCase()
  if (!id || seen[lower] || /(?:^|[-_.])(image|video|seedance|sora|veo|kling|hailuo)(?:$|[-_.])/.test(lower)) return
  seen[lower] = true
  var slot = authKind === 'apikey' ? apiSlots[lower] : null
  var sourceSlug = slot ? slot.profile : (lower === 'grok-4.6' ? 'gpt-5.4-mini' : lower)
  var source = bySlug[sourceSlug]
  if (!source && /-pro$/.test(sourceSlug)) source = bySlug[sourceSlug.slice(0, -4)]
  source = source || fallback
  var entry = JSON.parse(JSON.stringify(source))
  entry.slug = slot ? slot.shell : id
  entry.display_name = id
  entry.description = 'Available through MadAPI: ' + id
  entry.priority = result.length + 1
  entry.visibility = 'list'
  entry.supported_in_api = true
  entry.prefer_websockets = false
  if (entry.model_messages && typeof entry.model_messages === 'object') delete entry.model_messages.token_budget
  result.push(entry)
})
if (!result.length) throw new Error('MadAPI returned no Codex conversation models')
var outputPath = ObjC.unwrap($.NSProcessInfo.processInfo.environment.objectForKey('OUTPUT_PATH'))
var output = $(JSON.stringify({models: result}, null, 2))
var ok = output.writeToFileAtomicallyEncodingError(outputPath, true, $.NSUTF8StringEncoding, null)
if (!ok) throw new Error('Unable to write Codex model catalog')
JXA

mv -f "$output_path" "$catalog_path"
chmod 600 "$catalog_path"
rm -f "$models_cache_path"
trap - EXIT
rm -f "$available_path" "$template_path"
printf 'MadAPI Codex model catalog refreshed: %s\n' "$auth_kind"
