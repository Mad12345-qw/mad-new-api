#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: $0 <clone-dir> <db-sha256> <image-sha256> <loaded-tag> <preserve-tag>" >&2
  exit 64
fi

clone_dir="$1"
db_sha256="$2"
image_sha256="$3"
loaded_tag="$4"
preserve_tag="$5"
database="$clone_dir/one-api.db"
image_archive="$clone_dir/production-new-api-image.tar.gz"

printf '%s  %s\n' "$db_sha256" "$database" | sha256sum -c -
printf '%s  %s\n' "$image_sha256" "$image_archive" | sha256sum -c -

old_latest=""
if docker image inspect mad-new-api:latest >/dev/null 2>&1; then
  old_latest="$(docker image inspect --format '{{.Id}}' mad-new-api:latest)"
  docker tag "$old_latest" "$preserve_tag"
fi

load_output="$(gzip -dc "$image_archive" | docker image load)"
loaded_reference="$(printf '%s\n' "$load_output" | sed -n \
  -e 's/^Loaded image ID: //p' \
  -e 's/^Loaded image: //p' | tail -n 1)"
if [[ -z "$loaded_reference" ]]; then
  echo "docker load did not report a loaded image" >&2
  exit 70
fi
production_image="$(docker image inspect --format '{{.Id}}' "$loaded_reference")"
docker tag "$production_image" "$loaded_tag"

if [[ -n "$old_latest" ]]; then
  docker tag "$old_latest" mad-new-api:latest
fi

python3 - "$database" <<'PY'
import sqlite3
import sys

database = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
try:
    check = database.execute("PRAGMA quick_check").fetchone()[0]
    tables = database.execute(
        "SELECT COUNT(*) FROM sqlite_master "
        "WHERE type='table' AND name NOT LIKE 'sqlite_%'"
    ).fetchone()[0]
finally:
    database.close()

if check != "ok":
    raise SystemExit(f"SQLite quick_check failed: {check}")
print(f"sqlite_quick_check=ok tables={tables}")
PY

echo "production_image=$production_image loaded_tag=$loaded_tag"
echo "preserved_test_image=${old_latest:-none} preserve_tag=$preserve_tag"
