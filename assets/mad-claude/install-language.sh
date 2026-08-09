#!/bin/sh
set -eu

archive_url='https://codeload.github.com/javaht/claude-desktop-zh-cn/zip/b862463'
archive_sha256='9371cf89bb89ea5453cace672dfaa1f57b10d4fd09fea72a96b079b916cb8822'
stage_root=$(mktemp -d "${TMPDIR:-/tmp}/madapi-claude-language.XXXXXX")
archive_path="$stage_root/language-pack.zip"
source_root="$stage_root/source"
mkdir -p "$source_root"
trap 'rm -rf "$stage_root"' EXIT HUP INT TERM

if [ -n "${MADAPI_CLAUDE_LANGUAGE_ARCHIVE:-}" ]; then
  cp "$MADAPI_CLAUDE_LANGUAGE_ARCHIVE" "$archive_path"
else
  curl -fsSL --max-time 120 "$archive_url" -o "$archive_path"
fi
actual_hash=$(shasum -a 256 "$archive_path" | awk '{print $1}')
[ "$actual_hash" = "$archive_sha256" ] || { printf '%s\n' 'Claude Chinese archive checksum mismatch.' >&2; exit 1; }
/usr/bin/unzip -q "$archive_path" -d "$source_root"
project=$(find "$source_root" -mindepth 1 -maxdepth 1 -type d | head -1)
[ -n "$project" ]
[ -f "$project/scripts/patch_claude_zh_cn.py" ]
[ -f "$project/resources/frontend-zh-CN.json" ]
/usr/bin/python3 -m json.tool "$project/resources/frontend-zh-CN.json" >/dev/null

if [ "${MADAPI_CLAUDE_LANGUAGE_DRY_RUN:-0}" = 1 ]; then
  /usr/bin/python3 -m py_compile "$project/scripts/patch_claude_zh_cn.py"
  printf '%s\n' 'Claude Chinese macOS package verification passed.'
  exit 0
fi

app_path=${MADAPI_CLAUDE_APP_PATH:-/Applications/Claude.app}
[ -d "$app_path" ] || { printf '%s\n' 'Claude.app was not found.' >&2; exit 1; }
if ! sudo /usr/bin/python3 "$project/scripts/patch_claude_zh_cn.py" \
  --app "$app_path" \
  --user-home "$HOME" \
  --lang zh-CN \
  --skip-asar-patch \
  --launch
then
  sudo /usr/bin/python3 "$project/scripts/patch_claude_zh_cn.py" \
    --app "$app_path" \
    --user-home "$HOME" \
    --restore >/dev/null 2>&1 || true
  printf '%s\n' 'Claude Chinese safe-mode install failed and rollback was attempted.' >&2
  exit 1
fi

[ -f "$app_path/Contents/Resources/ion-dist/i18n/zh-CN.json" ]
locale=$(/usr/bin/plutil -extract locale raw -o - "$HOME/Library/Application Support/Claude-3p/config.json" 2>/dev/null || true)
[ "$locale" = 'zh-CN' ] || { printf '%s\n' 'Claude Chinese locale verification failed.' >&2; exit 1; }
printf '%s\n' 'Claude Chinese interface installed in safe mode.'
