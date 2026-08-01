#!/bin/sh
set -eu

COMPOSE_DIR=/opt/new-api
COMPOSE_FILE=$COMPOSE_DIR/docker-compose.yml
ENV_FILE=$COMPOSE_DIR/.env
SERVICE=cpa-codex
IMAGE=mad-cpa-codex:latest
HEALTH_URL=http://127.0.0.1:8318/healthz
RELEASE_BASE=https://github.com/Mad12345-qw/mad-new-api/releases/download/build-latest
STATE_FILE=$COMPOSE_DIR/mad-cpa-codex-sha256.txt
NGINX_PATCH=/usr/local/lib/mad-cpa-codex/patch-nginx.py

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
cache_bust=$(date +%s)

for asset in mad-cpa-codex.tar.gz patch-cpa-codex-nginx.py; do
  curl -fL --retry 3 --connect-timeout 15 --max-time 900 \
    -o "$work_dir/$asset" "$RELEASE_BASE/$asset?cb=$cache_bust"
  curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
    -o "$work_dir/$asset.sha256" "$RELEASE_BASE/$asset.sha256?cb=$cache_bust"
done

cd "$work_dir"
sha256sum -c mad-cpa-codex.tar.gz.sha256
sha256sum -c patch-cpa-codex-nginx.py.sha256
release_sha=$(awk '{print $1}' mad-cpa-codex.tar.gz.sha256)

install -d -m 0755 "$(dirname "$NGINX_PATCH")"
install -m 0755 "$work_dir/patch-cpa-codex-nginx.py" "$NGINX_PATCH"

needs_new_api_recreate=0
if ! grep -q '^MADAPI_INTERNAL_CATALOG_TOKEN=' "$ENV_FILE"; then
  token=$(od -An -N 32 -tx1 /dev/urandom | tr -d ' \n')
  printf '\nMADAPI_INTERNAL_CATALOG_TOKEN=%s\n' "$token" >> "$ENV_FILE"
  needs_new_api_recreate=1
fi

backup_dir="$COMPOSE_DIR/backups/cpa-codex-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$backup_dir"
cp -a "$COMPOSE_FILE" "$ENV_FILE" "$backup_dir/"

if ! grep -q '^  cpa-codex:$' "$COMPOSE_FILE"; then
  needs_new_api_recreate=1
fi

python3 - "$COMPOSE_FILE" <<'PY'
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
text = path.read_text(encoding="utf-8")
if "  cpa-codex:\n" not in text:
    if "      SESSION_SECRET: ${SESSION_SECRET}\n" not in text:
        raise SystemExit("missing NewAPI SESSION_SECRET environment line")
    text = text.replace(
        "      SESSION_SECRET: ${SESSION_SECRET}\n",
        "      SESSION_SECRET: ${SESSION_SECRET}\n      MADAPI_INTERNAL_CATALOG_TOKEN: ${MADAPI_INTERNAL_CATALOG_TOKEN}\n",
        1,
    )
    text += """
  cpa-codex:
    image: mad-cpa-codex:latest
    container_name: cpa-codex
    restart: unless-stopped
    ports:
      - \"127.0.0.1:8318:8317\"
    volumes:
      - ./cpa-codex:/data
    environment:
      TZ: Asia/Shanghai
      MADAPI_INTERNAL_URL: http://new-api:3000
      MADAPI_INTERNAL_CATALOG_TOKEN: ${MADAPI_INTERNAL_CATALOG_TOKEN}
    depends_on:
      new-api:
        condition: service_healthy
"""
    path.write_text(text, encoding="utf-8")
PY

if ! docker compose -f "$COMPOSE_FILE" config >/dev/null; then
  cp -a "$backup_dir/docker-compose.yml" "$COMPOSE_FILE"
  cp -a "$backup_dir/.env" "$ENV_FILE"
  exit 2
fi

needs_cpa_recreate=0
if [ ! -f "$STATE_FILE" ] || [ "$(cat "$STATE_FILE")" != "$release_sha" ] || ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  gzip -dc "$work_dir/mad-cpa-codex.tar.gz" | docker load
  needs_cpa_recreate=1
fi
if ! docker inspect -f '{{.State.Running}}' "$SERVICE" 2>/dev/null | grep -qx true; then
  needs_cpa_recreate=1
fi

if [ "$needs_new_api_recreate" -eq 1 ]; then
  if ! docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps new-api; then
    cp -a "$backup_dir/docker-compose.yml" "$COMPOSE_FILE"
    cp -a "$backup_dir/.env" "$ENV_FILE"
    docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps new-api || true
    exit 2
  fi
fi

new_api_healthy=0
for _ in $(seq 1 60); do
  if curl -fsS --max-time 3 http://127.0.0.1:3001/api/status 2>/dev/null | grep -q '"success":true'; then
    new_api_healthy=1
    break
  fi
  sleep 2
done
if [ "$new_api_healthy" -ne 1 ]; then
  cp -a "$backup_dir/docker-compose.yml" "$COMPOSE_FILE"
  cp -a "$backup_dir/.env" "$ENV_FILE"
  docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps new-api || true
  exit 2
fi

if [ "$needs_cpa_recreate" -eq 1 ]; then
  if ! docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps "$SERVICE"; then
    cp -a "$backup_dir/docker-compose.yml" "$COMPOSE_FILE"
    cp -a "$backup_dir/.env" "$ENV_FILE"
    docker rm -f "$SERVICE" || true
    docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps new-api || true
    exit 2
  fi
fi

healthy=0
for _ in $(seq 1 60); do
  if curl -fsS --max-time 3 "$HEALTH_URL" >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 2
done
if [ "$healthy" -ne 1 ]; then
  cp -a "$backup_dir/docker-compose.yml" "$COMPOSE_FILE"
  cp -a "$backup_dir/.env" "$ENV_FILE"
  docker rm -f "$SERVICE" || true
  docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps new-api || true
  exit 2
fi

if ! python3 "$NGINX_PATCH"; then
  exit 2
fi

printf '%s\n' "$release_sha" > "$STATE_FILE"
logger -t new-api-autoupdate "CPA Codex sidecar deployed successfully: $release_sha"
