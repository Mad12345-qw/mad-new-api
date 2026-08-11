#!/bin/sh
set -eu

codex_home=${CODEX_HOME:-$HOME/.codex}
catalog_path=$codex_home/madapi-cockpit-model-catalog.json
models_cache_path=$codex_home/models_cache.json
api_key=${MADAPI_API_KEY:-${MADAPI_KEY:-}}
base_url=${MADAPI_BASE_URL:-https://mad.myddns.me}
base_url=${base_url%/}
[ -n "$api_key" ] || { printf '%s\n' 'MADAPI_API_KEY is missing.' >&2; exit 1; }

mkdir -p "$codex_home"
available_path=$codex_home/madapi-models.$$.json
template_path=$codex_home/madapi-cpa-model-templates.$$.json
output_path=$codex_home/madapi-cockpit-model-catalog.$$.tmp
trap 'rm -f "$available_path" "$template_path" "$output_path"' EXIT

if [ -n "${MADAPI_REFRESH_RESPONSE_FILE:-}" ]; then
  cp "$MADAPI_REFRESH_RESPONSE_FILE" "$available_path"
else
  curl -fsS -H "Authorization: Bearer $api_key" "$base_url/codex/v1/models" -o "$available_path"
fi
if [ -n "${MADAPI_CODEX_TEMPLATE_FILE:-}" ]; then
  cp "$MADAPI_CODEX_TEMPLATE_FILE" "$template_path"
else
  curl -fsS 'https://models.router-for.me/codex_client_models.json' -o "$template_path"
fi

AVAILABLE_PATH="$available_path" TEMPLATE_PATH="$template_path" OUTPUT_PATH="$output_path" /usr/bin/osascript -l JavaScript <<'JXA'
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
if (!templates.length) throw new Error('The official CPA Codex model catalog is empty')
var bySlug = {}
templates.forEach(function (item) {
  if (item && typeof item.slug === 'string') bySlug[item.slug.toLowerCase()] = item
})
var fallback = bySlug['gpt-5.5'] || templates[0]
var sourceModels = Array.isArray(available.data) ? available.data : (Array.isArray(available.models) ? available.models : [])
var seen = {}
var result = []
sourceModels.forEach(function (item) {
  var id = item && typeof item.id === 'string' ? item.id : (item && typeof item.slug === 'string' ? item.slug : '')
  id = id.trim()
  var lower = id.toLowerCase()
  if (!id || seen[lower] || /(?:^|[-_.])(image|video|seedance|sora|veo|kling|hailuo)(?:$|[-_.])/.test(lower)) return
  seen[lower] = true
  var source = bySlug[lower]
  if (!source && /-pro$/.test(lower)) source = bySlug[lower.slice(0, -4)]
  source = source || fallback
  var entry = JSON.parse(JSON.stringify(source))
  entry.slug = id
  entry.display_name = id
  entry.priority = result.length + 1
  entry.visibility = 'list'
  entry.supported_in_api = true
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
printf '%s\n' 'MadAPI Codex model catalog refreshed.'
