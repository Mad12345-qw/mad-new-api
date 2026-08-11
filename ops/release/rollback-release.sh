#!/usr/bin/env bash
set -Eeuo pipefail

edge_root="${MADAPI_EDGE_ROOT:-/opt/madapi-release-edge}"
state_file="$edge_root/state.env"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ ! -f "$state_file" ]]; then
  echo "release state does not exist: $state_file" >&2
  exit 66
fi

previous="$(sed -n 's/^PREVIOUS=//p' "$state_file" | head -n 1)"
if [[ -z "$previous" ]]; then
  echo "release state has no previous target" >&2
  exit 65
fi

exec "$script_dir/switch-release.sh" "$previous"
