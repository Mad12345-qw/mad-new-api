#!/bin/sh
set -eu

script_path=${1:?image skill path is required}
script_path=$(CDPATH= cd -- "$(dirname -- "$script_path")" && pwd)/$(basename -- "$script_path")
root=$(mktemp -d "${TMPDIR:-/tmp}/madapi-image-skill-mock.XXXXXX")
trap 'rm -rf "$root"' EXIT HUP INT TERM

fake_bin=$root/bin
home=$root/home
work=$root/work
mkdir -p "$fake_bin" "$home/.codex" "$work"
printf '%s\n' 'sk-mock-image-test' > "$home/.codex/madapi.key"

cat > "$fake_bin/osascript" <<'FAKE_OSASCRIPT'
#!/bin/sh
set -eu
script=$(cat)
if printf '%s' "$script" | grep -Fq "writeToFileAtomicallyEncodingError(get('REQUEST')"; then
  printf '%s' '{"model":"gpt-image-2","prompt":"mock prompt","n":1,"quality":"auto","size":"auto","response_format":"url"}' > "$REQUEST"
elif printf '%s' "$script" | grep -Fq 'initWithBase64EncodedStringOptions'; then
  printf '%s' 'iVBORw0KGgo=' | base64 --decode > "$OUTPUT_PATH"
elif printf '%s' "$script" | grep -Fq 'typeof body.data[0].b64_json'; then
  printf '%s' 'iVBORw0KGgo='
elif printf '%s' "$script" | grep -Fq 'typeof body.data[0].url'; then
  printf '%s' ''
else
  printf '%s' '{"ok":true,"model":"gpt-image-2","path":"'"$PATH_VALUE"'","source_url":"'"${URL_VALUE:-}"'"}'
fi
FAKE_OSASCRIPT
chmod 700 "$fake_bin/osascript"

cat > "$fake_bin/curl" <<'FAKE_CURL'
#!/bin/sh
set -eu
output=
while [ "$#" -gt 0 ]; do
  arg=$1
  case "$arg" in
    -o) shift; output=${1:-} ;;
    -o*) output=${arg#-o} ;;
  esac
  shift
done
if [ -z "$output" ]; then exit 2; fi
printf '%s' '{"data":[{"b64_json":"iVBORw0KGgo="}]}' > "$output"
FAKE_CURL
chmod 700 "$fake_bin/curl"

cat > "$fake_bin/file" <<'FAKE_FILE'
#!/bin/sh
printf '%s\n' 'image/png'
FAKE_FILE
chmod 700 "$fake_bin/file"

cat > "$fake_bin/uuidgen" <<'FAKE_UUID'
#!/bin/sh
printf '%s\n' '00000000-0000-0000-0000-000000000001'
FAKE_UUID
chmod 700 "$fake_bin/uuidgen"

output=$(cd "$work" && \
  CODEX_HOME="$home/.codex" \
  MADAPI_BASE_URL='http://mock.invalid' \
  MADAPI_OSASCRIPT_BIN="$fake_bin/osascript" \
  MADAPI_CURL_BIN="$fake_bin/curl" \
  MADAPI_FILE_BIN="$fake_bin/file" \
  MADAPI_UUIDGEN_BIN="$fake_bin/uuidgen" \
  /bin/sh "$script_path" 'mock prompt')

printf '%s' "$output" | grep -Fq '"model":"gpt-image-2"'
printf '%s' "$output" | grep -Fq '"source_url":""'
printf '%s' "$output" | grep -Fq '"ok":true'
test -f "$work/outputs/madapi-00000000000000000000000000000001.png"
test "$(wc -c < "$work/outputs/madapi-00000000000000000000000000000001.png" | tr -d ' ')" -eq 8
printf '%s\n' 'codex_image_skill_macos_b64_mock=passed'
