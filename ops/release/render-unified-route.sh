#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 4 ]]; then
  echo "usage: $0 <new-api-port> <cpa-port> <image-port> <output-file>" >&2
  exit 64
fi

new_api_port="$1"
cpa_port="$2"
image_port="$3"
output="$4"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

[[ "$new_api_port" =~ ^[0-9]+$ && "$new_api_port" -ge 1 && "$new_api_port" -le 65535 ]]
[[ "$cpa_port" =~ ^[0-9]+$ && "$cpa_port" -ge 1 && "$cpa_port" -le 65535 ]]
[[ "$image_port" =~ ^[0-9]+$ && "$image_port" -ge 1 && "$image_port" -le 65535 ]]

sed \
  -e "s|__NEW_API_PORT__|$new_api_port|g" \
  -e "s|__CPA_PORT__|$cpa_port|g" \
  -e "s|__IMAGE_PORT__|$image_port|g" \
  "$script_dir/nginx-unified-route.conf.template" >"$output"

grep -Fq "proxy_pass http://127.0.0.1:$new_api_port;" "$output"
grep -Fq "proxy_pass http://127.0.0.1:$cpa_port;" "$output"
grep -Fq "proxy_pass http://127.0.0.1:$image_port;" "$output"
grep -Fq "location ^~ /v1/" "$output"
grep -Fq "location ^~ /codex/v1/" "$output"
! grep -Fq "new-api-codex-control" "$output"
