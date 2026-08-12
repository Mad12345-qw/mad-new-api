#!/bin/sh
set -eu

prompt=${1:-}
quality=${MADAPI_IMAGE_QUALITY:-auto}
size=${MADAPI_IMAGE_SIZE:-auto}
[ -n "$prompt" ] || { printf '%s\n' 'Prompt cannot be empty.' >&2; exit 1; }

codex_home=${CODEX_HOME:-"$HOME/.codex"}
key_path=$codex_home/madapi.key
output_dir=$codex_home/generated_images
base_url=${MADAPI_BASE_URL:-https://mad.myddns.me}
base_url=${base_url%/}
[ -f "$key_path" ] || { printf '%s\n' 'MadAPI key file is missing.' >&2; exit 1; }
api_key=$(tr -d '\r\n' < "$key_path")
case "$api_key" in sk-*[!A-Za-z0-9._-]*|'') printf '%s\n' 'MadAPI key file is invalid.' >&2; exit 1 ;; esac

mkdir -p "$output_dir"
request=$(mktemp "${TMPDIR:-/tmp}/madapi-image-request.XXXXXX")
response=$(mktemp "${TMPDIR:-/tmp}/madapi-image-response.XXXXXX")
download=$(mktemp "${TMPDIR:-/tmp}/madapi-image-download.XXXXXX")
trap 'rm -f "$request" "$response" "$download"' EXIT HUP INT TERM

PROMPT="$prompt" QUALITY="$quality" SIZE="$size" REQUEST="$request" /usr/bin/osascript -l JavaScript <<'JXA'
ObjC.import('Foundation')
var env = $.NSProcessInfo.processInfo.environment
function get(name) { return ObjC.unwrap(env.objectForKey(name)) }
var payload = {model:'gpt-image-2',prompt:get('PROMPT'),n:1,quality:get('QUALITY'),size:get('SIZE'),response_format:'url'}
var text = $(JSON.stringify(payload))
if (!text.writeToFileAtomicallyEncodingError(get('REQUEST'), true, $.NSUTF8StringEncoding, null)) throw new Error('Unable to write request')
JXA

curl -fsS --max-time 300 -H "Authorization: Bearer $api_key" -H 'Content-Type: application/json; charset=utf-8' --data-binary "@$request" "$base_url/v1/images/generations" -o "$response"
image_url=$(RESPONSE="$response" /usr/bin/osascript -l JavaScript <<'JXA'
ObjC.import('Foundation')
var path = ObjC.unwrap($.NSProcessInfo.processInfo.environment.objectForKey('RESPONSE'))
var data = $.NSData.dataWithContentsOfFile(path)
var text = ObjC.unwrap($.NSString.alloc.initWithDataEncoding(data, $.NSUTF8StringEncoding))
var body = JSON.parse(text)
if (!body.data || !body.data[0] || typeof body.data[0].url !== 'string' || !body.data[0].url) throw new Error('MadAPI returned no image URL')
body.data[0].url
JXA
)
curl -fsSL --max-time 300 "$image_url" -o "$download"
kind=$(file -b --mime-type "$download")
case "$kind" in image/png) ext=png ;; image/jpeg) ext=jpg ;; image/webp) ext=webp ;; *) printf '%s\n' 'Downloaded payload is not a supported image.' >&2; exit 1 ;; esac
output=$output_dir/madapi-$(uuidgen | tr '[:upper:]' '[:lower:]' | tr -d -).$ext
mv "$download" "$output"
chmod 600 "$output"
PATH_VALUE="$output" URL_VALUE="$image_url" /usr/bin/osascript -l JavaScript <<'JXA'
var env = $.NSProcessInfo.processInfo.environment
var path = ObjC.unwrap(env.objectForKey('PATH_VALUE'))
var url = ObjC.unwrap(env.objectForKey('URL_VALUE'))
JSON.stringify({ok:true,model:'gpt-image-2',path:path,source_url:url,preview_markdown:'![Generated image]('+url+')',download_markdown:'[Open or download original]('+url+')'})
JXA
