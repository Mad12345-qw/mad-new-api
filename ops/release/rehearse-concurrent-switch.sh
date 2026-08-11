#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -lt 1 || $# -gt 3 ]]; then
  echo "usage: $0 <work-root> [requests] [concurrency]" >&2
  exit 64
fi

work_root="$1"
requests="${2:-2000}"
concurrency="${3:-50}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
edge_container="${MADAPI_EDGE_CONTAINER:-madapi-release-edge-rehearsal}"
edge_url="${MADAPI_EDGE_URL:-http://127.0.0.1:13018}"
probe_path="${MADAPI_SWITCH_PROBE_PATH:-/}"

if [[ ! "$requests" =~ ^[1-9][0-9]*$ ]]; then
  echo "requests must be a positive integer" >&2
  exit 64
fi
if [[ ! "$concurrency" =~ ^[1-9][0-9]*$ ]]; then
  echo "concurrency must be a positive integer" >&2
  exit 64
fi

export MADAPI_EDGE_ROOT="$work_root"
export MADAPI_EDGE_CONTAINER="$edge_container"
export MADAPI_EDGE_URL="$edge_url"

result="$(mktemp)"
trap 'rm -f "$result"' EXIT

probe_worker() {
  local worker="$1"
  local request_index
  local status
  for ((request_index = worker; request_index <= requests; request_index += concurrency)); do
    status="$(curl --silent --show-error --max-time 5 \
      --output /dev/null --write-out '%{http_code}' \
      "$edge_url$probe_path" || true)"
    printf '%s\n' "${status:-000}" >>"$result"
  done
}

"$script_dir/switch-release.sh" candidate >/dev/null

for ((worker = 1; worker <= concurrency; worker++)); do
  probe_worker "$worker" &
done

for target in baseline candidate baseline candidate baseline candidate; do
  "$script_dir/switch-release.sh" "$target" >/dev/null
  sleep 0.15
done

wait

total="$(wc -l <"$result")"
ok="$(awk '$1 ~ /^2[0-9][0-9]$/ {n++} END {print n+0}' "$result")"
limited="$(awk '$1 == "429" {n++} END {print n+0}' "$result")"
server_error="$(awk '$1 ~ /^5[0-9][0-9]$/ {n++} END {print n+0}' "$result")"
network_error="$(awk '$1 == "000" {n++} END {print n+0}' "$result")"
other=$((total - ok - limited - server_error - network_error))

printf '{"requests":%s,"http_2xx":%s,"http_429":%s,"http_5xx":%s,"network_errors":%s,"other":%s,"switches":6,"final_active":"candidate"}\n' \
  "$total" "$ok" "$limited" "$server_error" "$network_error" "$other"

test "$total" -eq "$requests"
test "$server_error" -eq 0
test "$network_error" -eq 0
test "$other" -eq 0
