#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <unified-backup-directory>" >&2
  exit 64
fi

backup_dir="$1"
site="${MADAPI_NGINX_SITE:-/etc/nginx/sites-enabled/mad.myddns.me}"
new_api_port="${MADAPI_NEW_API_PORT:-3001}"
health_attempts="${MADAPI_UNIFIED_HEALTH_ATTEMPTS:-120}"
network="${MADAPI_DOCKER_NETWORK:-new-api_default}"

[[ -d "$backup_dir" ]]
[[ -f "$backup_dir/nginx.site.before.conf" ]]
[[ -f "$backup_dir/release.env" ]]
for command in docker curl nginx; do
  command -v "$command" >/dev/null || {
    echo "required command is unavailable: $command" >&2
    exit 69
  }
done

get_release_value() {
  local key="$1"
  sed -n "s/^${key}=//p" "$backup_dir/release.env" | tail -n 1
}

old_new_api="$(get_release_value MADAPI_UNIFIED_OLD_NEW_API)"
old_cpa="$(get_release_value MADAPI_UNIFIED_OLD_CPA)"
old_control="$(get_release_value MADAPI_UNIFIED_OLD_CONTROL)"
old_cpa_present="$(get_release_value MADAPI_UNIFIED_OLD_CPA_PRESENT)"
old_control_present="$(get_release_value MADAPI_UNIFIED_OLD_CONTROL_PRESENT)"
[[ "$old_new_api" =~ ^[A-Za-z0-9._-]+$ ]]
[[ "$old_cpa" =~ ^[A-Za-z0-9._-]+$ || -z "$old_cpa" ]]
[[ "$old_control" =~ ^[A-Za-z0-9._-]+$ || -z "$old_control" ]]
[[ "$old_cpa_present" == 0 || "$old_cpa_present" == 1 ]]
[[ "$old_control_present" == 0 || "$old_control_present" == 1 ]]

if docker inspect "$old_new_api" >/dev/null 2>&1; then
  echo "rollback target already exists: $old_new_api" >&2
  exit 73
fi

docker rm -f new-api cpa-official-gateway >/dev/null 2>&1 || true
docker rename "$old_new_api" new-api
docker network connect "$network" new-api >/dev/null 2>&1 || true
if [[ "$old_cpa_present" == 1 ]]; then
  docker rename "$old_cpa" cpa-official-gateway
fi
if [[ "$old_control_present" == 1 ]]; then
  docker rename "$old_control" new-api-codex-control
fi

docker start new-api >/dev/null
if [[ "$old_cpa_present" == 1 ]]; then
  docker start cpa-official-gateway >/dev/null
fi
if [[ "$old_control_present" == 1 ]]; then
  docker start new-api-codex-control >/dev/null
fi

cp -a "$backup_dir/nginx.site.before.conf" "$site"
nginx -t
nginx -s reload

status=""
for _ in $(seq 1 "$health_attempts"); do
  status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 \
    "http://127.0.0.1:$new_api_port/api/status" || true)"
  [[ "$status" =~ ^2[0-9][0-9]$ ]] && break
  sleep 0.5
done
[[ "$status" =~ ^2[0-9][0-9]$ ]]
printf 'unified_rollback=ok backup=%s status=%s\n' "$backup_dir" "$status"
