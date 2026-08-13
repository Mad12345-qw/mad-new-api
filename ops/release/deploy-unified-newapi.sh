#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <release-directory> <git-sha>" >&2
  exit 64
fi

release_dir="$1"
git_sha="$2"
script_dir="$(cd "$(dirname "$0")" && pwd)"
network="${MADAPI_DOCKER_NETWORK:-new-api_default}"
env_file="${MADAPI_ENV_FILE:-/opt/madapi-releases/7c90de45/production-runtime/production.env}"
data_dir="${MADAPI_DATA_DIR:-/opt/new-api/data}"
database="${MADAPI_SQLITE_DATABASE:-$data_dir/one-api.db}"
log_dir="${MADAPI_LOG_DIR:-/opt/new-api/logs}"
old_port="${MADAPI_NEW_API_PORT:-3001}"
candidate_port="${MADAPI_CANDIDATE_NEW_API_PORT:-13001}"
image_port="${MADAPI_IMAGE_PORT:-3013}"
image_compat_port="${MADAPI_IMAGE_COMPAT_PORT:-3010}"
image_gateway_binary="${MADAPI_IMAGE_GATEWAY_BINARY:-/opt/image-media-gateway/image-media-gateway}"
image_gateway_release_binary="$release_dir/image-media-gateway"
image_gateway_service="${MADAPI_IMAGE_GATEWAY_SERVICE:-image-media-gateway.service}"
image_gateway_dropin="${MADAPI_IMAGE_GATEWAY_DROPIN:-/etc/systemd/system/image-media-gateway.service.d/20-unified-upstream.conf}"
site="${MADAPI_NGINX_SITE:-/etc/nginx/sites-enabled/mad.myddns.me}"
backup_root="${MADAPI_UNIFIED_BACKUP_ROOT:-/opt/madapi-release-backups}"
lock_file="${MADAPI_UNIFIED_LOCK_FILE:-/run/lock/madapi-unified-deploy.lock}"
health_attempts="${MADAPI_UNIFIED_HEALTH_ATTEMPTS:-120}"

[[ $(id -u) -eq 0 ]]
[[ "$git_sha" =~ ^[0-9a-f]{40}$ ]]
[[ -d "$release_dir" && -f "$release_dir/SHA256SUMS" && -f "$release_dir/release-manifest.json" && -f "$image_gateway_release_binary" && -f "$env_file" && -f "$site" && -d "$data_dir" && -f "$database" ]]
[[ -x "$image_gateway_binary" && -f "$image_gateway_dropin" ]]
[[ "$(dirname "$database")" == "$data_dir" ]]
for value in "$old_port" "$candidate_port" "$image_port" "$image_compat_port" "$health_attempts"; do [[ "$value" =~ ^[1-9][0-9]*$ ]]; done
[[ "$old_port" != "$candidate_port" ]]
for command in docker gzip curl nginx sha256sum python3 flock cp systemctl; do command -v "$command" >/dev/null; done
control_token="$(sed -n 's/^MADAPI_CPA_CONTROL_TOKEN=//p' "$env_file" | tail -n 1)"
[[ ${#control_token} -ge 32 ]]
if grep -Eq '^[[:space:]]*(SQL_DSN|DATABASE_URL)=' "$env_file"; then
  echo "unified SQLite release script refuses a shared external database candidate" >&2
  exit 78
fi

exec 9>"$lock_file"
flock -n 9 || { echo "another unified deployment is active" >&2; exit 75; }

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup_dir="$backup_root/unified-$timestamp"
snapshot_data="$backup_dir/candidate-data"
snapshot_logs="$backup_dir/candidate-logs"
candidate_new="new-api-candidate-$timestamp"
candidate_cpa="cpa-official-gateway-candidate-$timestamp"
candidate_new_alias="new-api-candidate-$timestamp"
candidate_cpa_alias="cpa-candidate-$timestamp"
old_new_backup="new-api-rollback-$timestamp"
old_cpa_backup="cpa-official-gateway-rollback-$timestamp"
old_control_backup="new-api-codex-control-rollback-$timestamp"
deployed_new_backup="new-api-deployed-$timestamp"
deployed_cpa_backup="cpa-official-gateway-deployed-$timestamp"
mkdir -p "$backup_dir" "$snapshot_data" "$snapshot_logs"
chmod 700 "$backup_dir"
(cd "$release_dir" && sha256sum -c SHA256SUMS --ignore-missing)

docker inspect new-api >"$backup_dir/new-api.inspect.json"
docker inspect cpa-official-gateway >"$backup_dir/cpa-official-gateway.inspect.json" 2>/dev/null || true
docker inspect new-api-codex-control >"$backup_dir/new-api-codex-control.inspect.json" 2>/dev/null || true
cp -a "$env_file" "$backup_dir/production.env"
cp -a "$site" "$backup_dir/nginx.site.before.conf"
cp -a "$image_gateway_dropin" "$backup_dir/image-gateway-upstream.before.conf"
cp -a "$image_gateway_binary" "$backup_dir/image-gateway-binary.before"
cp -a "$image_gateway_release_binary" "$backup_dir/image-gateway-binary.candidate"
sha256sum "$image_gateway_binary" >"$backup_dir/image-gateway-binary.sha256"
database_name="$(basename "$database")"
python3 - "$data_dir" "$snapshot_data" "$database_name" <<'PY'
import os
import shutil
import sys

source_root, target_root, database_name = sys.argv[1:]
excluded = {database_name, database_name + "-wal", database_name + "-shm"}
for entry in os.scandir(source_root):
    if entry.name in excluded:
        continue
    destination = os.path.join(target_root, entry.name)
    if entry.is_dir(follow_symlinks=False):
        shutil.copytree(entry.path, destination, symlinks=True, dirs_exist_ok=True)
    else:
        shutil.copy2(entry.path, destination, follow_symlinks=False)
PY
python3 - "$database" "$snapshot_data/$database_name" <<'PY'
import sqlite3
import sys

source = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True, timeout=30)
target = sqlite3.connect(sys.argv[2], timeout=30)
with target:
    source.backup(target)
if target.execute("pragma integrity_check").fetchone()[0] != "ok":
    raise SystemExit("SQLite online backup failed integrity_check")
target.close()
source.close()
PY
nginx -T >"$backup_dir/nginx.before.txt" 2>&1

old_cpa_present=0
old_control_present=0
final_started=0
old_stopped=0
site_switched=0
image_gateway_switched=0
image_gateway_binary_switched=0
tmp_route="$(mktemp)"
tmp_site="$(mktemp)"
cleanup_files() { rm -f "$tmp_route" "$tmp_site"; }
restore_old() {
  set +e
  docker rm -f "$candidate_new" "$candidate_cpa" >/dev/null 2>&1 || true
  if [[ "$old_stopped" -eq 1 ]]; then
    docker rename "$old_new_backup" new-api >/dev/null 2>&1 || true
    [[ "$old_cpa_present" -eq 1 ]] && docker rename "$old_cpa_backup" cpa-official-gateway >/dev/null 2>&1 || true
    [[ "$old_control_present" -eq 1 ]] && docker rename "$old_control_backup" new-api-codex-control >/dev/null 2>&1 || true
    docker start new-api >/dev/null 2>&1 || true
    [[ "$old_cpa_present" -eq 1 ]] && docker start cpa-official-gateway >/dev/null 2>&1 || true
    [[ "$old_control_present" -eq 1 ]] && docker start new-api-codex-control >/dev/null 2>&1 || true
  fi
  if [[ "$site_switched" -eq 1 ]]; then
    cp -a "$backup_dir/nginx.site.before.conf" "$site"
    nginx -t >/dev/null 2>&1 && nginx -s reload >/dev/null 2>&1 || true
  fi
  if [[ "$image_gateway_switched" -eq 1 ]]; then
    if [[ "$image_gateway_binary_switched" -eq 1 ]]; then
      cp -a "$backup_dir/image-gateway-binary.before" "$image_gateway_binary"
    fi
    cp -a "$backup_dir/image-gateway-upstream.before.conf" "$image_gateway_dropin"
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl restart "$image_gateway_service" >/dev/null 2>&1 || true
  fi
}
fail_deploy() {
  status="$1"
  restore_old
  cleanup_files
  echo "unified deployment failed; old production restored; backup=$backup_dir" >&2
  exit "$status"
}
trap 'status=$?; fail_deploy "$status"' ERR INT TERM

gzip -dc "$release_dir/mad-new-api.tar.gz" | docker load >/dev/null
gzip -dc "$release_dir/mad-cpa-official-gateway.tar.gz" | docker load >/dev/null
new_api_image="mad-new-api:$git_sha"
cpa_image="mad-cpa-official-gateway:$git_sha"
manifest_commit="$(python3 - "$release_dir/release-manifest.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    print(json.load(handle).get("madapi_commit", ""))
PY
)"
[[ "$manifest_commit" == "$git_sha" ]]
docker image inspect mad-new-api:latest >/dev/null
docker image inspect mad-cpa-official-gateway:latest >/dev/null
docker tag mad-new-api:latest "$new_api_image"
docker tag mad-cpa-official-gateway:latest "$cpa_image"
docker image inspect "$new_api_image" >/dev/null
docker image inspect "$cpa_image" >/dev/null
docker network inspect "$network" >/dev/null

start_pair() {
  pair_data="$1"
  pair_logs="$2"
  pair_port="$3"
  docker run -d --name "$candidate_cpa" --network "$network" --network-alias "$candidate_cpa_alias" \
    --memory 256m --memory-swap 384m --cpus 0.75 --pids-limit 128 --env-file "$env_file" \
    -e TZ=Asia/Shanghai -e MADAPI_NEWAPI_CONTROL_URL="http://$candidate_new_alias:3000/internal/madapi/cpa" \
    -e MADAPI_CPA_PORT=18317 \
    -e MADAPI_CPA_EXECUTE_PORT=18417 "$cpa_image" >/dev/null
  docker run -d --name "$candidate_new" --network "$network" --network-alias "$candidate_new_alias" \
    --memory 768m --memory-swap 1024m --cpus 1.25 --pids-limit 256 \
    -p "127.0.0.1:$pair_port:3000" --env-file "$env_file" \
    -e TZ=Asia/Shanghai -e NODE_NAME=new-api-unified-candidate \
    -e MADAPI_CPA_HANDLER_URL="http://$candidate_cpa_alias:18417/execute" \
    -e MADAPI_GEMINI_IMAGE_CONCURRENCY=1 -e GOMEMLIMIT=512MiB -e GOGC=50 \
    -v "$pair_data:/data" -v "$pair_logs:/app/logs" "$new_api_image" --log-dir /app/logs >/dev/null
}

check_pair() {
  pair_port="$1"
  cpa_status=""; execute_status=""; new_status=""; codex_status=""
  for _ in $(seq 1 "$health_attempts"); do
    cpa_status="$(docker run --rm --network "$network" curlimages/curl:8.12.1 -sS -o /dev/null -w '%{http_code}' --max-time 3 "http://$candidate_cpa_alias:18417/healthz" || true)"
    execute_status="$(docker run --rm --network "$network" curlimages/curl:8.12.1 -sS -o /dev/null -w '%{http_code}' --max-time 3 -X POST -H "X-MadAPI-CPA-Execute-Token: $control_token" --data-binary 'invalid-frame' "http://$candidate_cpa_alias:18417/execute" || true)"
    new_status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:$pair_port/api/status" || true)"
    codex_status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 -X POST -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:$pair_port/codex/v1/responses" || true)"
    [[ "$cpa_status" == 200 && "$execute_status" == 400 && "$new_status" == 200 && "$codex_status" =~ ^[34][0-9][0-9]$ ]] && return 0
    sleep 0.5
  done
  return 1
}

# Preflight uses a private SQLite snapshot; it cannot write production state.
start_pair "$snapshot_data" "$snapshot_logs" "$candidate_port"
check_pair "$candidate_port"
docker rm -f "$candidate_new" "$candidate_cpa" >/dev/null

# Final pair is the only writer after all old application writers stop.
if docker inspect new-api-codex-control >/dev/null 2>&1; then old_control_present=1; fi
old_writers=(new-api)
[[ "$old_control_present" -eq 0 ]] || old_writers+=(new-api-codex-control)
docker stop "${old_writers[@]}" >/dev/null
old_stopped=1
docker rename new-api "$old_new_backup"
if docker inspect cpa-official-gateway >/dev/null 2>&1; then docker stop cpa-official-gateway >/dev/null; docker rename cpa-official-gateway "$old_cpa_backup"; old_cpa_present=1; fi
[[ "$old_control_present" -eq 0 ]] || docker rename new-api-codex-control "$old_control_backup"
# Final writers always use stable names so a subsequent release can discover
# the active pair without consulting an older release record.
candidate_new="new-api"
candidate_cpa="cpa-official-gateway"
start_pair "$data_dir" "$log_dir" "$old_port"
final_started=1
check_pair "$old_port"

cat >"$image_gateway_dropin.tmp" <<EOF
[Service]
Environment="UPSTREAM=http://127.0.0.1:$image_compat_port"
EOF
chmod --reference="$image_gateway_dropin" "$image_gateway_dropin.tmp"
cp -a "$image_gateway_dropin.tmp" "$backup_dir/image-gateway-upstream.candidate.conf"
mv -f "$image_gateway_dropin.tmp" "$image_gateway_dropin"
image_gateway_switched=1
cp -a "$image_gateway_release_binary" "$image_gateway_binary.tmp"
chmod --reference="$image_gateway_binary" "$image_gateway_binary.tmp"
chown --reference="$image_gateway_binary" "$image_gateway_binary.tmp"
mv -f "$image_gateway_binary.tmp" "$image_gateway_binary"
image_gateway_binary_switched=1
systemctl daemon-reload
systemctl is-active --quiet image-url-compat.service
systemctl restart "$image_gateway_service"
systemctl is-active --quiet "$image_gateway_service"
[[ "$(systemctl show "$image_gateway_service" -p Environment --value | tr ' ' '\n' | grep '^UPSTREAM=' | tail -1)" == "UPSTREAM=http://127.0.0.1:$image_compat_port" ]]
image_status=""
for _ in $(seq 1 30); do
  image_status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 \
    "http://127.0.0.1:$image_port/health" || true)"
  [[ "$image_status" == 200 ]] && break
  sleep 0.5
done
[[ "$image_status" == 200 ]]

bash "$script_dir/render-unified-route.sh" "$old_port" "$image_port" "$tmp_route"
python3 "$script_dir/patch-nginx-unified-route.py" "$site" "$tmp_route" "$tmp_site"
cp -a "$tmp_site" "$site"
site_switched=1
nginx -t
nginx -s reload
public_url="${MADAPI_PUBLIC_URL:-}"
if [[ -n "$public_url" ]]; then
  edge_status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 15 "${public_url%/}/codex/v1" || true)"
  [[ "$edge_status" =~ ^[234][0-9][0-9]$ ]]
fi

cat >"$backup_dir/release.env" <<EOF
MADAPI_UNIFIED_COMMIT=$git_sha
MADAPI_UNIFIED_BACKUP=$backup_dir
MADAPI_UNIFIED_OLD_NEW_API=$old_new_backup
MADAPI_UNIFIED_OLD_CPA=$old_cpa_backup
MADAPI_UNIFIED_OLD_CONTROL=$old_control_backup
MADAPI_UNIFIED_OLD_CPA_PRESENT=$old_cpa_present
MADAPI_UNIFIED_OLD_CONTROL_PRESENT=$old_control_present
MADAPI_UNIFIED_OLD_PORT=$old_port
MADAPI_UNIFIED_CANDIDATE_NEW_API=$candidate_new
MADAPI_UNIFIED_CANDIDATE_CPA=$candidate_cpa
MADAPI_UNIFIED_DEPLOYED_NEW_BACKUP=$deployed_new_backup
MADAPI_UNIFIED_DEPLOYED_CPA_BACKUP=$deployed_cpa_backup
MADAPI_UNIFIED_CANDIDATE_PORT=$old_port
MADAPI_UNIFIED_IMAGE_GATEWAY_DROPIN=$image_gateway_dropin
MADAPI_UNIFIED_IMAGE_GATEWAY_SERVICE=$image_gateway_service
MADAPI_UNIFIED_IMAGE_GATEWAY_SHA256=$(sha256sum "$image_gateway_binary" | awk '{print $1}')
EOF
chmod 600 "$backup_dir/release.env"
trap - ERR INT TERM
cleanup_files
printf 'unified_deployment=ok commit=%s backup=%s active_port=%s preflight_port=%s sqlite_single_writer=true\n' "$git_sha" "$backup_dir" "$old_port" "$candidate_port"
