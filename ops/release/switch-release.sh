#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <target-name>" >&2
  exit 64
fi

target_name="$1"
edge_root="${MADAPI_EDGE_ROOT:-/opt/madapi-release-edge}"
nginx_container="${MADAPI_EDGE_CONTAINER:-madapi-release-edge}"
edge_url="${MADAPI_EDGE_URL:-http://127.0.0.1:13018}"
target_file="$edge_root/targets/$target_name.conf"
active_link="$edge_root/active-upstream.conf"
state_file="$edge_root/state.env"

is_healthy_status() {
  local status="$1"
  [[ "$status" =~ ^[0-9]{3}$ ]] && \
    ((10#$status >= 200 && 10#$status < 500))
}

if [[ ! "$target_name" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "invalid target name: $target_name" >&2
  exit 64
fi
if [[ ! -f "$target_file" ]]; then
  echo "target file does not exist: $target_file" >&2
  exit 66
fi

direct_url="$(sed -n 's/^# direct_url=//p' "$target_file" | head -n 1)"
if [[ -z "$direct_url" ]]; then
  echo "target file has no direct_url marker: $target_file" >&2
  exit 65
fi

health_path="${MADAPI_HEALTH_PATH:-/api/status}"
direct_status="$(curl --silent --show-error --max-time 10 \
  --output /dev/null --write-out '%{http_code}' \
  "$direct_url$health_path" || true)"
if ! is_healthy_status "$direct_status"; then
  echo "direct health check failed: status=$direct_status" >&2
  exit 69
fi

old_link=""
if [[ -L "$active_link" ]]; then
  old_link="$(readlink "$active_link")"
fi
old_active=""
old_previous=""
if [[ -f "$state_file" ]]; then
  old_active="$(sed -n 's/^ACTIVE=//p' "$state_file" | head -n 1)"
  old_previous="$(sed -n 's/^PREVIOUS=//p' "$state_file" | head -n 1)"
fi

restore_old_config() {
  if [[ -n "$old_link" ]]; then
    local restore_link="$edge_root/.active-upstream.restore.$$"
    local restore_headers
    local restore_body
    local restored_header
    local restored_status
    local expected_old="$old_active"
    ln -s "$old_link" "$restore_link"
    mv -Tf "$restore_link" "$active_link"
    docker exec "$nginx_container" nginx -t >/dev/null 2>&1 || true
    docker exec "$nginx_container" nginx -s reload >/dev/null 2>&1 || true
    if [[ -z "$expected_old" ]]; then
      expected_old="$(basename "$old_link" .conf)"
    fi
    restore_headers="$(mktemp)"
    restore_body="$(mktemp)"
    for _ in $(seq 1 40); do
      restored_status="$(curl --silent --show-error --max-time 5 \
          --dump-header "$restore_headers" --output "$restore_body" \
          --write-out '%{http_code}' "$edge_url$health_path" || true)"
      if is_healthy_status "$restored_status"; then
        restored_header="$(awk 'BEGIN { IGNORECASE=1 } /^X-MadAPI-Active-Release:/ { gsub("\\r", "", $2); print $2 }' "$restore_headers" | tail -n 1)"
        if [[ "$restored_header" == "$expected_old" ]]; then
          rm -f "$restore_headers" "$restore_body"
          return 0
        fi
      fi
      sleep 0.25
    done
    rm -f "$restore_headers" "$restore_body"
    echo "critical: previous target did not become healthy after restore" >&2
    return 1
  fi
  echo "critical: no previous target is available for restore" >&2
  return 1
}

new_link="$edge_root/.active-upstream.new.$$"
ln -s "targets/$target_name.conf" "$new_link"
mv -Tf "$new_link" "$active_link"

if ! docker exec "$nginx_container" nginx -t >/dev/null; then
  restore_old_config || true
  echo "nginx validation failed; the previous target was restored" >&2
  exit 78
fi

docker exec "$nginx_container" nginx -s reload >/dev/null

headers="$(mktemp)"
body="$(mktemp)"
trap 'rm -f "$headers" "$body"' EXIT

edge_ok=false
for _ in $(seq 1 20); do
  edge_status="$(curl --silent --show-error --max-time 5 \
      --dump-header "$headers" --output "$body" \
      --write-out '%{http_code}' "$edge_url$health_path" || true)"
  if is_healthy_status "$edge_status"; then
    active_header="$(awk 'BEGIN { IGNORECASE=1 } /^X-MadAPI-Active-Release:/ { gsub("\\r", "", $2); print $2 }' "$headers" | tail -n 1)"
    if [[ "$active_header" == "$target_name" ]]; then
      edge_ok=true
      break
    fi
  fi
  sleep 0.25
done

if [[ "$edge_ok" != true ]]; then
  if ! restore_old_config; then
    echo "automatic recovery could not verify the previous edge" >&2
    exit 70
  fi
  echo "edge health check failed; the previous target was restored" >&2
  exit 69
fi

previous="$old_previous"
if [[ -n "$old_active" && "$old_active" != "$target_name" ]]; then
  previous="$old_active"
fi

state_tmp="$edge_root/.state.$$"
{
  printf 'ACTIVE=%s\n' "$target_name"
  printf 'PREVIOUS=%s\n' "$previous"
  printf 'SWITCHED_AT=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} >"$state_tmp"
mv -Tf "$state_tmp" "$state_file"

echo "active=$target_name previous=$previous"
