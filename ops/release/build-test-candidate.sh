#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <source-root> <image-tag> <build-log>" >&2
  exit 64
fi

source_root="$1"
image_tag="$2"
build_log="$3"

if [[ ! -f "$source_root/Dockerfile" ]]; then
  echo "Dockerfile does not exist under source-root" >&2
  exit 66
fi

for script in "$source_root"/ops/release/*.sh; do
  bash -n "$script"
done
python3 -m py_compile "$source_root/ops/release/sqlite-clone-fingerprint.py"

docker build --tag "$image_tag" "$source_root" >"$build_log" 2>&1
docker image inspect --format '{{.Id}}|{{json .RepoTags}}|{{.Size}}' "$image_tag"
