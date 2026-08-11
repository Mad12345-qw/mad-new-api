#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <cpa-config.yaml>" >&2
  exit 64
fi

config_path="$1"
if [[ ! -f "$config_path" ]]; then
  echo "CPA config does not exist: $config_path" >&2
  exit 66
fi

mode="$(sed -n -E 's/^[[:space:]]*disable-image-generation:[[:space:]]*([^#[:space:]]+).*$/\1/p' "$config_path" | tail -n 1)"
mode="${mode%\"}"
mode="${mode#\"}"
mode="${mode%\'}"
mode="${mode#\'}"

if [[ "$mode" != "passthrough" ]]; then
  echo "CPA disable-image-generation must be passthrough; got: ${mode:-missing}" >&2
  exit 65
fi

echo "CPA image generation mode is passthrough."
