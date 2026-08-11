#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -lt 1 || $# -gt 4 ]]; then
  echo "usage: $0 <work-root> [baseline-port] [candidate-port] [edge-port]" >&2
  exit 64
fi

work_root="$1"
baseline_port="${2:-13016}"
candidate_port="${3:-13017}"
edge_port="${4:-13018}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
edge_container="madapi-release-edge-rehearsal"

if [[ ! "$work_root" = /* ]]; then
  echo "work-root must be an absolute path" >&2
  exit 64
fi
if [[ "$work_root" == "/" ]]; then
  echo "work-root cannot be /" >&2
  exit 64
fi

mkdir -p "$work_root/targets"
sed "s/__EDGE_PORT__/$edge_port/g" \
  "$script_dir/nginx-edge.conf.template" >"$work_root/nginx.conf"

write_target() {
  local name="$1"
  local direct_port="$2"
  local upstream_port="$3"
  cat >"$work_root/targets/$name.conf" <<EOF
# direct_url=http://127.0.0.1:$direct_port
upstream madapi_active {
    server 127.0.0.1:$upstream_port max_fails=1 fail_timeout=3s;
    keepalive 128;
}
map \$host \$madapi_release { default "$name"; }
EOF
}

write_target baseline "$baseline_port" "$baseline_port"
write_target candidate "$candidate_port" "$candidate_port"
write_target broken_after_switch "$baseline_port" 1
ln -sfn targets/baseline.conf "$work_root/active-upstream.conf"
rm -f "$work_root/state.env"

docker rm -f "$edge_container" >/dev/null 2>&1 || true
docker run --detach --name "$edge_container" --network host \
  --volume "$work_root:/etc/nginx/madapi-edge:ro" \
  --volume "$work_root/nginx.conf:/etc/nginx/nginx.conf:ro" \
  nginx:1.28-alpine >/dev/null

export MADAPI_EDGE_ROOT="$work_root"
export MADAPI_EDGE_CONTAINER="$edge_container"
export MADAPI_EDGE_URL="http://127.0.0.1:$edge_port"

started_at="$(date +%s%3N)"
"$script_dir/switch-release.sh" baseline
"$script_dir/switch-release.sh" candidate
"$script_dir/rollback-release.sh"
"$script_dir/switch-release.sh" candidate

if "$script_dir/switch-release.sh" broken_after_switch; then
  echo "broken target unexpectedly became active" >&2
  exit 70
fi

headers="$(mktemp)"
trap 'rm -f "$headers"' EXIT
active_header=""
for _ in $(seq 1 40); do
  if curl --fail --silent --show-error --max-time 5 \
      --dump-header "$headers" --output /dev/null \
      "http://127.0.0.1:$edge_port/api/status"; then
    active_header="$(awk 'BEGIN { IGNORECASE=1 } /^X-MadAPI-Active-Release:/ { gsub("\\r", "", $2); print $2 }' "$headers" | tail -n 1)"
    if [[ "$active_header" == "candidate" ]]; then
      break
    fi
  fi
  sleep 0.25
done
if [[ "$active_header" != "candidate" ]]; then
  echo "automatic recovery failed: active=$active_header" >&2
  exit 70
fi

finished_at="$(date +%s%3N)"
duration_ms=$((finished_at - started_at))
result_file="$work_root/rehearsal-result.json"
cat >"$result_file" <<EOF
{
  "passed": true,
  "baseline_port": $baseline_port,
  "candidate_port": $candidate_port,
  "edge_port": $edge_port,
  "final_active": "candidate",
  "explicit_rollback": "passed",
  "post_switch_failure_auto_recovery": "passed",
  "duration_ms": $duration_ms,
  "completed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

cat "$result_file"
