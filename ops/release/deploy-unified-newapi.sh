#!/usr/bin/env bash
set -Eeuo pipefail

# Deploy one NewAPI plus the independent official CPA gateway.
# deploy-codex-control-only.sh remains the legacy emergency definition.

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <release-directory> <git-sha>" >&2
  exit 64
fi

release_dir="$1"
git_sha="$2"
network="${MADAPI_DOCKER_NETWORK:-new-api_default}"
env_file="${MADAPI_ENV_FILE:-/opt/madapi-releases/7c90de45/production-runtime/production.env}"
data_dir="${MADAPI_DATA_DIR:-/opt/new-api/data}"
log_dir="${MADAPI_LOG_DIR:-/opt/new-api/logs}"
new_api_port="${MADAPI_NEW_API_PORT:-3001}"
cpa_port="${MADAPI_CPA_PORT:-8330}"
image_port="${MADAPI_IMAGE_PORT:-3013}"
site="${MADAPI_NGINX_SITE:-/etc/nginx/sites-enabled/mad.myddns.me}"
backup_root="${MADAPI_UNIFIED_BACKUP_ROOT:-/opt/madapi-release-backups}"
lock_file="${MADAPI_UNIFIED_LOCK_FILE:-/run/lock/madapi-unified-deploy.lock}"
health_attempts="${MADAPI_UNIFIED_HEALTH_ATTEMPTS:-120}"
script_dir="$(cd "$(dirname "$0")" && pwd)"

[[ $(id -u) -eq 0 ]]
[[ "$git_sha" =~ ^[0-9a-f]{40}$ ]]
[[ -d "$release_dir" && -f "$release_dir/SHA256SUMS" && -f "$env_file" && -f "$site" ]]
[[ "$new_api_port" =~ ^[0-9]+$ && "$cpa_port" =~ ^[0-9]+$ && "$image_port" =~ ^[0-9]+$ ]]
[[ "$health_attempts" =~ ^[1-9][0-9]*$ ]]
for command in docker gzip curl nginx sha256sum python3 flock; do
  command -v "$command" >/dev/null || {
    echo "required command is unavailable: $command" >&2
    exit 69
  }
done

exec 9>"$lock_file"
flock -n 9 || {
  echo "another unified deployment is active" >&2
  exit 75
}

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup_dir="$backup_root/unified-$timestamp"
mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
(cd "$release_dir" && sha256sum -c SHA256SUMS --ignore-missing)

old_new_api_backup="new-api-unified-rollback-$timestamp"
old_cpa_backup="cpa-official-gateway-unified-rollback-$timestamp"
old_control_backup="new-api-codex-control-unified-rollback-$timestamp"
old_control_present=0
old_cpa_present=0
new_api_replaced=0
cpa_replaced=0

docker inspect new-api >"$backup_dir/new-api.inspect.json"
docker inspect cpa-official-gateway >"$backup_dir/cpa-official-gateway.inspect.json" 2>/dev/null || true
docker inspect new-api-codex-control >"$backup_dir/new-api-codex-control.inspect.json" 2>/dev/null || true
cp -a "$env_file" "$backup_dir/production.env"
cp -a "$site" "$backup_dir/nginx.site.before.conf"
nginx -T >"$backup_dir/nginx.before.txt" 2>&1

rollback() {
  local status="$1"
  set +e
  echo "unified deployment failed; restoring $backup_dir" >&2
  if [[ "$new_api_replaced" -eq 1 ]]; then
    docker rm -f new-api >/dev/null 2>&1 || true
    docker rename "$old_new_api_backup" new-api >/dev/null 2>&1 || true
    docker network connect "$network" new-api >/dev/null 2>&1 || true
  fi
  if [[ "$cpa_replaced" -eq 1 ]]; then
    docker rm -f cpa-official-gateway >/dev/null 2>&1 || true
    docker rename "$old_cpa_backup" cpa-official-gateway >/dev/null 2>&1 || true
  fi
  docker rename "$old_control_backup" new-api-codex-control >/dev/null 2>&1 || true
  docker start new-api >/dev/null 2>&1 || true
  docker start cpa-official-gateway >/dev/null 2>&1 || true
  docker start new-api-codex-control >/dev/null 2>&1 || true
  cp -a "$backup_dir/nginx.site.before.conf" "$site" >/dev/null 2>&1 || true
  nginx -t >/dev/null 2>&1 && nginx -s reload >/dev/null 2>&1 || true
  echo "rollback_attempted=$backup_dir" >&2
  exit "$status"
}
trap 'status=$?; rollback "$status"' ERR INT TERM

gzip -dc "$release_dir/mad-new-api.tar.gz" | docker load >/dev/null
gzip -dc "$release_dir/mad-cpa-official-gateway.tar.gz" | docker load >/dev/null
new_api_image="mad-new-api:$git_sha"
cpa_image="mad-cpa-official-gateway:$git_sha"
docker image inspect "$new_api_image" >/dev/null
docker image inspect "$cpa_image" >/dev/null
docker network inspect "$network" >/dev/null

tmp_route="$(mktemp)"
tmp_site="$(mktemp)"
trap 'rm -f "$tmp_route" "$tmp_site"' EXIT
"$script_dir/render-unified-route.sh" "$new_api_port" "$cpa_port" "$image_port" "$tmp_route"
python3 "$script_dir/patch-nginx-unified-route.py" "$site" "$tmp_route" "$tmp_site"
cp -a "$tmp_site" "$site"
if ! nginx -t >/dev/null 2>&1; then
  cp -a "$backup_dir/nginx.site.before.conf" "$site"
  rollback 78
fi

if docker inspect new-api >/dev/null 2>&1; then
  docker stop new-api >/dev/null
  docker rename new-api "$old_new_api_backup"
  docker network disconnect "$network" "$old_new_api_backup" >/dev/null 2>&1 || true
  new_api_replaced=1
fi
docker run -d \
  --name new-api \
  --restart unless-stopped \
  --network "$network" \
  --network-alias new-api \
  --memory 768m --memory-swap 1024m --cpus 1.25 --pids-limit 256 \
  -p "127.0.0.1:$new_api_port:3000" \
  --env-file "$env_file" \
  -e TZ=Asia/Shanghai \
  -e NODE_NAME=new-api-unified \
  -e MADAPI_GEMINI_IMAGE_CONCURRENCY=1 \
  -e GOMEMLIMIT=512MiB -e GOGC=50 \
  -v "$data_dir:/data" -v "$log_dir:/app/logs" \
  "$new_api_image" --log-dir /app/logs >/dev/null || rollback 70

status=""
for _ in $(seq 1 "$health_attempts"); do
  status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 \
    "http://127.0.0.1:$new_api_port/api/status" || true)"
  [[ "$status" =~ ^2[0-9][0-9]$ ]] && break
  sleep 0.5
done
[[ "$status" =~ ^2[0-9][0-9]$ ]] || rollback 69

if docker inspect cpa-official-gateway >/dev/null 2>&1; then
  docker stop cpa-official-gateway >/dev/null
  docker rename cpa-official-gateway "$old_cpa_backup"
  old_cpa_present=1
  cpa_replaced=1
fi
docker run -d \
  --name cpa-official-gateway \
  --restart unless-stopped \
  --network "$network" \
  --memory 256m --memory-swap 384m --cpus 0.75 --pids-limit 128 \
  -p "127.0.0.1:$cpa_port:18317" \
  --env-file "$env_file" \
  -e MADAPI_NEWAPI_CONTROL_URL=http://new-api:3000/internal/madapi/cpa \
  "$cpa_image" >/dev/null || rollback 70

sleep 2
docker inspect cpa-official-gateway >/dev/null || rollback 69

nginx -s reload
public_url="${MADAPI_PUBLIC_URL:-}"
if [[ -n "$public_url" ]]; then
  curl -fsS --max-time 15 "$public_url/api/status" >/dev/null || rollback 69
fi

if docker inspect new-api-codex-control >/dev/null 2>&1; then
  docker stop new-api-codex-control >/dev/null
  docker rename new-api-codex-control "$old_control_backup"
  old_control_present=1
fi

trap - ERR INT TERM
cat >"$backup_dir/release.env" <<EOF
MADAPI_UNIFIED_COMMIT=$git_sha
MADAPI_UNIFIED_BACKUP=$backup_dir
MADAPI_UNIFIED_OLD_NEW_API=$old_new_api_backup
MADAPI_UNIFIED_OLD_CPA=$old_cpa_backup
MADAPI_UNIFIED_OLD_CONTROL=$old_control_backup
MADAPI_UNIFIED_OLD_CPA_PRESENT=$old_cpa_present
MADAPI_UNIFIED_OLD_CONTROL_PRESENT=$old_control_present
EOF
chmod 600 "$backup_dir/release.env"
printf 'unified_deployment=ok commit=%s backup=%s old_control_preserved=%s\n' \
  "$git_sha" "$backup_dir" "$old_control_present"
