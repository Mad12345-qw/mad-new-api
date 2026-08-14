#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <newapi-only-backup-directory>" >&2
  exit 64
fi

backup_dir="$(cd "$1" && pwd)"
state="$backup_dir/rollback.env"
[[ $(id -u) -eq 0 && -f "$state" && -f "$backup_dir/SHA256SUMS" ]]
(cd "$backup_dir" && sha256sum -c SHA256SUMS --ignore-missing)

get_value() { sed -n "s/^$1=//p" "$state" | tail -n 1; }
current="$(get_value MADAPI_NEWAPI_ONLY_CURRENT)"
rollback="$(get_value MADAPI_NEWAPI_ONLY_ROLLBACK)"
failed="$(get_value MADAPI_NEWAPI_ONLY_FAILED)"
port="$(get_value MADAPI_NEWAPI_ONLY_PORT)"
nginx_site="$(get_value MADAPI_NEWAPI_ONLY_NGINX_SITE)"
nginx_sha="$(get_value MADAPI_NEWAPI_ONLY_NGINX_SHA256)"
image_compat_health_url="${MADAPI_IMAGE_COMPAT_HEALTH_URL:-http://127.0.0.1:3010/health}"
image_gateway_health_url="${MADAPI_IMAGE_GATEWAY_HEALTH_URL:-http://127.0.0.1:3013/health}"
for value in "$current" "$rollback" "$failed"; do [[ "$value" =~ ^[A-Za-z0-9._-]+$ ]]; done
[[ "$port" =~ ^[1-9][0-9]*$ ]]
[[ "$(sha256sum "$nginx_site" | awk '{print $1}')" == "$nginx_sha" ]]
(cd / && sha256sum -c "$backup_dir/protected-surfaces.before.sha256")
docker inspect "$current" >/dev/null
docker inspect "$rollback" >/dev/null

docker stop "$current" >/dev/null
docker rename "$current" "$failed"
docker rename "$rollback" "$current"
docker start "$current" >/dev/null
status=""
for _ in $(seq 1 120); do
  status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:$port/api/status" || true)"
  [[ "$status" == 200 ]] && break
  sleep 0.5
done
if [[ "$status" != 200 ]]; then
  docker stop "$current" >/dev/null 2>&1 || true
  docker rename "$current" "$rollback" >/dev/null 2>&1 || true
  docker rename "$failed" "$current" >/dev/null 2>&1 || true
  docker start "$current" >/dev/null 2>&1 || true
  echo "rollback target failed health; candidate was restored" >&2
  exit 69
fi
[[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "$image_compat_health_url" || true)" == 200 ]]
[[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "$image_gateway_health_url" || true)" == 200 ]]
cmp -s "$backup_dir/config/nginx.full.conf" <(nginx -T 2>/dev/null)
(cd / && sha256sum -c "$backup_dir/protected-surfaces.before.sha256")
nginx -t
echo "newapi_only_rollback=ok backup=$backup_dir health=$status"
