#!/bin/sh
set -eu

COMPOSE_DIR=/opt/new-api
COMPOSE_FILE=$COMPOSE_DIR/docker-compose.yml
ENV_FILE=$COMPOSE_DIR/.env
SERVICE=cpa-codex
IMAGE=mad-cpa-codex:latest
HEALTH_URL=http://127.0.0.1:8318/healthz
NEW_API_HEALTH_URL=http://127.0.0.1:3001/api/status
RELEASE_BASE=https://github.com/Mad12345-qw/mad-new-api/releases/download/build-latest
STATE_FILE=$COMPOSE_DIR/mad-cpa-codex-sha256.txt
INSTALL_DIR=/usr/local/lib/mad-cpa-codex
NGINX_PATCH=$INSTALL_DIR/patch-nginx.py
COMPOSE_RECONCILE=$INSTALL_DIR/compose-reconcile.py

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
cache_bust=$(date +%s)

for asset in mad-cpa-codex.tar.gz patch-cpa-codex-nginx.py compose-cpa-codex.py; do
  curl -fL --retry 3 --connect-timeout 15 --max-time 900 \
    -o "$work_dir/$asset" "$RELEASE_BASE/$asset?cb=$cache_bust"
  curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
    -o "$work_dir/$asset.sha256" "$RELEASE_BASE/$asset.sha256?cb=$cache_bust"
done

cd "$work_dir"
sha256sum -c mad-cpa-codex.tar.gz.sha256
sha256sum -c patch-cpa-codex-nginx.py.sha256
sha256sum -c compose-cpa-codex.py.sha256
release_sha=$(awk '{print $1}' mad-cpa-codex.tar.gz.sha256)

timestamp=$(date +%Y%m%d-%H%M%S)
backup_dir="$COMPOSE_DIR/backups/cpa-native-$timestamp"
mkdir -p "$backup_dir"
cp -a "$COMPOSE_FILE" "$ENV_FILE" "$backup_dir/"
before_config_sha=$(cat "$COMPOSE_FILE" "$ENV_FILE" | sha256sum | awk '{print $1}')

if ! grep -q '^MADAPI_CODEX_DISPATCH_TOKEN=' "$ENV_FILE"; then
  token=$(od -An -N 32 -tx1 /dev/urandom | tr -d ' \n')
  printf '\nMADAPI_CODEX_DISPATCH_TOKEN=%s\n' "$token" >> "$ENV_FILE"
fi

install -d -m 0755 "$INSTALL_DIR"
install -m 0755 "$work_dir/patch-cpa-codex-nginx.py" "$NGINX_PATCH"
install -m 0755 "$work_dir/compose-cpa-codex.py" "$COMPOSE_RECONCILE"
python3 "$COMPOSE_RECONCILE" "$COMPOSE_FILE"

if ! docker compose -f "$COMPOSE_FILE" config >/dev/null; then
  cp -a "$backup_dir/docker-compose.yml" "$COMPOSE_FILE"
  cp -a "$backup_dir/.env" "$ENV_FILE"
  exit 2
fi

after_config_sha=$(cat "$COMPOSE_FILE" "$ENV_FILE" | sha256sum | awk '{print $1}')
old_image_id=$(docker image inspect -f '{{.Id}}' "$IMAGE" 2>/dev/null || true)
rollback_tag="mad-cpa-codex:rollback-$timestamp"
if [ -n "$old_image_id" ]; then
  docker image tag "$old_image_id" "$rollback_tag"
fi

gzip -dc "$work_dir/mad-cpa-codex.tar.gz" | docker load
if ! docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps "$SERVICE"; then
  cp -a "$backup_dir/docker-compose.yml" "$COMPOSE_FILE"
  cp -a "$backup_dir/.env" "$ENV_FILE"
  if [ -n "$old_image_id" ]; then docker image tag "$rollback_tag" "$IMAGE"; fi
  docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps "$SERVICE" || true
  exit 2
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
  if [ -n "$old_image_id" ]; then docker image tag "$rollback_tag" "$IMAGE"; fi
  docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps "$SERVICE" || true
  exit 2
fi

if [ "$before_config_sha" != "$after_config_sha" ]; then
  if ! docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps new-api; then
    cp -a "$backup_dir/docker-compose.yml" "$COMPOSE_FILE"
    cp -a "$backup_dir/.env" "$ENV_FILE"
    if [ -n "$old_image_id" ]; then docker image tag "$rollback_tag" "$IMAGE"; fi
    docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps "$SERVICE" new-api || true
    exit 2
  fi
fi

new_api_healthy=0
for _ in $(seq 1 60); do
  if curl -fsS --max-time 3 "$NEW_API_HEALTH_URL" 2>/dev/null | grep -q '"success":true'; then
    new_api_healthy=1
    break
  fi
  sleep 2
done
if [ "$new_api_healthy" -ne 1 ]; then
  cp -a "$backup_dir/docker-compose.yml" "$COMPOSE_FILE"
  cp -a "$backup_dir/.env" "$ENV_FILE"
  if [ -n "$old_image_id" ]; then docker image tag "$rollback_tag" "$IMAGE"; fi
  docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps "$SERVICE" new-api || true
  exit 2
fi

python3 "$NGINX_PATCH"
printf '%s\n' "$release_sha" > "$STATE_FILE"
logger -t new-api-autoupdate "CPA native Codex executor deployed successfully: $release_sha"
