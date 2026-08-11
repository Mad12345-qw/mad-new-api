#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
compose_dir="${MADAPI_COMPOSE_DIR:-/opt/new-api}"
compose_file="${MADAPI_COMPOSE_FILE:-$compose_dir/docker-compose.yml}"
env_file="${MADAPI_ENV_FILE:-$compose_dir/.env}"
db_file="${MADAPI_DB_FILE:-$compose_dir/data/one-api.db}"
nginx_site="${MADAPI_NGINX_SITE:-/etc/nginx/sites-enabled/mad.myddns.me}"
nginx_container="${MADAPI_NGINX_CONTAINER:-}"
new_api_upstream="${MADAPI_NEW_API_UPSTREAM:-http://127.0.0.1:3001}"
nginx_new_api_upstream="${MADAPI_NGINX_NEW_API_UPSTREAM:-$new_api_upstream}"
health_url="${MADAPI_HEALTH_URL:-$new_api_upstream/api/status}"
release_base="${MADAPI_RELEASE_BASE:-https://github.com/Mad12345-qw/mad-new-api/releases/download/build-latest}"
release_dir="${MADAPI_RELEASE_DIR:-}"
lock_file="${MADAPI_DEPLOY_LOCK_FILE:-/run/lock/madapi-cpa-sdk-deploy.lock}"
backup_root="${MADAPI_BACKUP_ROOT:-$compose_dir/backups}"
sdk_health_attempts="${MADAPI_SDK_HEALTH_ATTEMPTS:-60}"
new_api_health_attempts="${MADAPI_NEW_API_HEALTH_ATTEMPTS:-90}"

for command in curl docker gzip openssl python3 sha256sum; do
  command -v "$command" >/dev/null || {
    echo "required command is unavailable: $command" >&2
    exit 69
  }
done
if [[ -n "$nginx_container" ]]; then
  docker inspect "$nginx_container" >/dev/null
else
  command -v nginx >/dev/null || {
    echo "required command is unavailable: nginx" >&2
    exit 69
  }
fi
test -f "$compose_file"
test -f "$env_file"
test -f "$nginx_site"
[[ "$sdk_health_attempts" =~ ^[1-9][0-9]*$ ]]
[[ "$new_api_health_attempts" =~ ^[1-9][0-9]*$ ]]

exec 9>"$lock_file"
flock -n 9 || {
  echo "another MadAPI deployment is active" >&2
  exit 75
}

work_dir="$(mktemp -d)"
timestamp="$(date +%Y%m%d-%H%M%S)"
backup_dir="$backup_root/cpa-sdk-boundary-$timestamp"
mkdir -p "$backup_dir"
chmod 700 "$backup_dir"

cleanup() {
  rm -rf "$work_dir"
}
trap cleanup EXIT

nginx_test() {
  if [[ -n "$nginx_container" ]]; then
    docker exec "$nginx_container" nginx -t
  else
    nginx -t
  fi
}

nginx_reload() {
  if [[ -n "$nginx_container" ]]; then
    docker exec "$nginx_container" nginx -s reload
  else
    nginx -s reload
  fi
}

assets=(release-manifest.json SHA256SUMS mad-new-api.tar.gz mad-cpa-sdk-host.tar.gz)
if [[ -n "$release_dir" ]]; then
  for asset in "${assets[@]}"; do
    cp -a "$release_dir/$asset" "$work_dir/$asset"
  done
else
  cache_bust="$(date +%s)"
  for asset in "${assets[@]}"; do
    curl -fL --retry 3 --connect-timeout 15 --max-time 1800 \
      -o "$work_dir/$asset" "$release_base/$asset?cb=$cache_bust"
  done
fi

cd "$work_dir"
grep -E '  (mad-new-api|mad-cpa-sdk-host)\.tar\.gz$' SHA256SUMS > required-sha256sums
test "$(wc -l < required-sha256sums)" -eq 2
sha256sum -c required-sha256sums
python3 - release-manifest.json <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    manifest = json.load(handle)
expected = {
    "release_format": 2,
    "cpa_version": "v7.2.128",
    "cpa_image_generation_mode": "passthrough",
    "cpa_sdk_host_image": "mad-cpa-sdk-host:latest",
}
for key, value in expected.items():
    if manifest.get(key) != value:
        raise SystemExit(f"release manifest mismatch for {key}")
PY

cp -a "$compose_file" "$backup_dir/docker-compose.yml"
cp -a "$env_file" "$backup_dir/.env"
candidate_env="$work_dir/.env"
cp -a "$env_file" "$candidate_env"
nginx_real="$(readlink -f "$nginx_site")"
cp -a "$nginx_real" "$backup_dir/nginx-site.conf"
if [[ -f "$db_file" ]]; then
  python3 - "$db_file" "$backup_dir/one-api.db" <<'PY'
import sqlite3
import sys

source = sqlite3.connect(sys.argv[1])
target = sqlite3.connect(sys.argv[2])
source.backup(target)
target.close()
source.close()
PY
fi

old_new_api_id="$(docker image inspect mad-new-api:latest --format '{{.Id}}')"
old_cpa_sdk_id="$(docker image inspect mad-cpa-sdk-host:latest --format '{{.Id}}' 2>/dev/null || true)"
docker image tag "$old_new_api_id" "mad-new-api:rollback-$timestamp"
if [[ -n "$old_cpa_sdk_id" ]]; then
  docker image tag "$old_cpa_sdk_id" "mad-cpa-sdk-host:rollback-$timestamp"
fi

upsert_env() {
  local key="$1"
  local value="$2"
  python3 - "$candidate_env" "$key" "$value" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
key = sys.argv[2]
value = sys.argv[3]
lines = path.read_text(encoding="utf-8").splitlines()
replacement = f"{key}={value}"
found = False
result = []
for line in lines:
    if line.startswith(key + "="):
        if not found:
            result.append(replacement)
            found = True
    else:
        result.append(line)
if not found:
    result.append(replacement)
path.write_text("\n".join(result) + "\n", encoding="utf-8")
PY
}

get_env() {
  local key="$1"
  sed -n "s/^${key}=//p" "$candidate_env" | tail -n 1
}

trusted_token="$(get_env TRUSTED_ROUTE_TOKEN)"
dispatch_token="$(get_env MADAPI_CPA_SDK_DISPATCH_TOKEN)"
if [[ ! "$trusted_token" =~ ^[A-Fa-f0-9]{64,}$ ]]; then
  trusted_token="$(openssl rand -hex 32)"
  upsert_env TRUSTED_ROUTE_TOKEN "$trusted_token"
fi
if [[ ! "$dispatch_token" =~ ^[A-Fa-f0-9]{64,}$ ]]; then
  dispatch_token="$(openssl rand -hex 32)"
  upsert_env MADAPI_CPA_SDK_DISPATCH_TOKEN "$dispatch_token"
fi
chmod 600 "$candidate_env"

candidate_compose="$work_dir/docker-compose.yml"
python3 "$script_dir/compose-cpa-sdk.py" "$compose_file" "$candidate_compose"
docker compose --project-directory "$compose_dir" --env-file "$candidate_env" \
  -f "$candidate_compose" config >/dev/null

snippet="$work_dir/codex-route.conf"
TRUSTED_ROUTE_TOKEN="$trusted_token" \
  "$script_dir/render-codex-route.sh" "$nginx_new_api_upstream" "$snippet" >/dev/null
candidate_nginx="$work_dir/nginx-site.conf"
python3 "$script_dir/patch-nginx-codex-route.py" "$nginx_real" "$snippet" "$candidate_nginx"

rollback_started=0
rollback() {
  local reason="$1"
  if [[ "$rollback_started" -eq 1 ]]; then
    return
  fi
  rollback_started=1
  set +e
  echo "deployment failed; restoring the previous release: $reason" >&2
  docker compose --project-directory "$compose_dir" --env-file "$env_file" \
    -f "$candidate_compose" stop new-api cpa-sdk-host >/dev/null 2>&1
  cp -a "$backup_dir/.env" "$env_file"
  cp -a "$backup_dir/docker-compose.yml" "$compose_file"
  cp -a "$backup_dir/nginx-site.conf" "$nginx_real"
  docker image tag "mad-new-api:rollback-$timestamp" mad-new-api:latest
  if [[ -n "$old_cpa_sdk_id" ]]; then
    docker image tag "mad-cpa-sdk-host:rollback-$timestamp" mad-cpa-sdk-host:latest
  fi
  if [[ -f "$backup_dir/one-api.db" ]]; then
    cp -a "$backup_dir/one-api.db" "$db_file"
  fi
  docker compose --project-directory "$compose_dir" --env-file "$env_file" \
    -f "$compose_file" up -d >/dev/null 2>&1
  if ! grep -q '^  cpa-sdk-host:' "$compose_file"; then
    docker rm -f cpa-sdk-host >/dev/null 2>&1
  fi
  restored_new_api=0
  for _ in $(seq 1 "$new_api_health_attempts"); do
    if curl -fsS --max-time 3 "$health_url" 2>/dev/null | grep -q '"success":true'; then
      restored_new_api=1
      break
    fi
    sleep 1
  done
  restored_sdk=1
  if grep -q '^  cpa-sdk-host:' "$compose_file"; then
    restored_sdk=0
    for _ in $(seq 1 "$sdk_health_attempts"); do
      if [[ "$(docker inspect cpa-sdk-host --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' 2>/dev/null)" == healthy ]]; then
        restored_sdk=1
        break
      fi
      sleep 1
    done
  fi
  nginx_ok=0
  if nginx_test >/dev/null 2>&1 && nginx_reload >/dev/null 2>&1; then
    nginx_ok=1
  fi
  if [[ "$restored_new_api" -eq 1 && "$restored_sdk" -eq 1 && "$nginx_ok" -eq 1 ]]; then
    echo "rollback completed and verified from $backup_dir" >&2
  else
    echo "rollback restored files but health verification failed: new_api=$restored_new_api sdk=$restored_sdk nginx=$nginx_ok" >&2
  fi
}

trap 'status=$?; rollback "line $LINENO, status $status"; exit $status' ERR
trap 'rollback "signal"; exit 130' INT TERM

gzip -dc mad-cpa-sdk-host.tar.gz | docker load >/dev/null
gzip -dc mad-new-api.tar.gz | docker load >/dev/null

cp -a "$candidate_env" "$env_file"
chmod 600 "$env_file"
docker compose --project-directory "$compose_dir" --env-file "$env_file" \
  -f "$candidate_compose" up -d --no-deps cpa-sdk-host
for _ in $(seq 1 "$sdk_health_attempts"); do
  if [[ "$(docker inspect cpa-sdk-host --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' 2>/dev/null)" == "healthy" ]]; then
    break
  fi
  sleep 1
done
test "$(docker inspect cpa-sdk-host --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}')" = healthy

cp -a "$candidate_compose" "$compose_file"
docker compose --project-directory "$compose_dir" --env-file "$env_file" \
  -f "$compose_file" up -d --force-recreate --no-deps new-api
for _ in $(seq 1 "$new_api_health_attempts"); do
  if curl -fsS --max-time 3 "$health_url" 2>/dev/null | grep -q '"success":true'; then
    break
  fi
  sleep 1
done
curl -fsS --max-time 5 "$health_url" | grep -q '"success":true'

cp -a "$candidate_nginx" "$nginx_real"
nginx_test
nginx_reload

wrong_status=000
for _ in $(seq 1 20); do
  wrong_status="$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' \
    -H 'X-New-Api-Route-Token: wrong' "$new_api_upstream/v1/models" || true)"
  if [[ "$wrong_status" != 000 && "${wrong_status:0:1}" != 5 ]]; then
    break
  fi
  sleep 0.5
done
correct_status=000
for _ in $(seq 1 20); do
  correct_status="$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' \
    -H "X-New-Api-Route-Token: $trusted_token" "$new_api_upstream/v1/models" || true)"
  if [[ "$correct_status" != 000 && "${correct_status:0:1}" != 5 ]]; then
    break
  fi
  sleep 0.5
done
if [[ "$wrong_status" == 000 || "${wrong_status:0:1}" == 5 || "$correct_status" == 000 || "${correct_status:0:1}" == 5 ]]; then
  echo "trusted route acceptance failed: wrong=$wrong_status correct=$correct_status" >&2
  false
fi

manifest_commit="$(python3 -c 'import json; print(json.load(open("release-manifest.json"))["madapi_commit"])')"
cat > "$compose_dir/mad-cpa-sdk-release.env" <<EOF
MADAPI_COMMIT=$manifest_commit
DEPLOYED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
BACKUP_DIR=$backup_dir
NEW_API_ROLLBACK_IMAGE=mad-new-api:rollback-$timestamp
CPA_SDK_ROLLBACK_IMAGE=mad-cpa-sdk-host:rollback-$timestamp
EOF
chmod 600 "$compose_dir/mad-cpa-sdk-release.env"

trap - ERR INT TERM
echo "deployment=ok commit=$manifest_commit backup=$backup_dir"
