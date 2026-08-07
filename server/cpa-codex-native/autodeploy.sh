#!/bin/sh
set -eu

COMPOSE_DIR=/opt/new-api
COMPOSE_FILE=$COMPOSE_DIR/docker-compose.yml
ENV_FILE=$COMPOSE_DIR/.env
NATIVE_SERVICE=cpa-codex-native
LEGACY_NATIVE_SERVICE=cpa-codex
LEGACY_COCKPIT_SERVICE=cpa-codex-cockpit
IMAGE=mad-cpa-codex:latest
NATIVE_HEALTH_URL=http://127.0.0.1:8320/healthz
RELEASE_BASE=https://github.com/Mad12345-qw/mad-new-api/releases/download/build-latest
STATE_FILE=$COMPOSE_DIR/mad-cpa-codex-sha256.txt
ROLLBACK_STATE_FILE=$COMPOSE_DIR/mad-cpa-codex-rollback.env
INSTALL_DIR=/usr/local/lib/mad-cpa-codex
NGINX_PATCH=$INSTALL_DIR/patch-nginx.py
COMPOSE_RECONCILE=$INSTALL_DIR/compose-reconcile.py
NGINX_CONFIG=/etc/nginx/sites-enabled/mad.myddns.me

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
cp -a "$COMPOSE_FILE" "$ENV_FILE" "$NGINX_CONFIG" "$backup_dir/"
if [ -f "$STATE_FILE" ]; then
  cp -a "$STATE_FILE" "$backup_dir/"
fi

rollback_tag="mad-cpa-codex:rollback-$timestamp"
old_image_id=$(docker image inspect -f '{{.Id}}' "$IMAGE" 2>/dev/null || true)
if [ -n "$old_image_id" ]; then
  docker image tag "$old_image_id" "$rollback_tag"
fi

rollback() {
  cp -a "$backup_dir/docker-compose.yml" "$COMPOSE_FILE"
  cp -a "$backup_dir/.env" "$ENV_FILE"
  cp -a "$backup_dir/mad.myddns.me" "$NGINX_CONFIG"
  nginx -t >/dev/null 2>&1 && systemctl reload nginx || true
  docker rm -f "$NATIVE_SERVICE" >/dev/null 2>&1 || true
  if [ -n "$old_image_id" ]; then
    docker image tag "$rollback_tag" "$IMAGE"
    docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps "$NATIVE_SERVICE"
  fi
}

install -d -m 0755 "$INSTALL_DIR"
install -m 0755 "$work_dir/patch-cpa-codex-nginx.py" "$NGINX_PATCH"
install -m 0755 "$work_dir/compose-cpa-codex.py" "$COMPOSE_RECONCILE"

python3 "$COMPOSE_RECONCILE" "$COMPOSE_FILE"
if ! docker compose -f "$COMPOSE_FILE" config >/dev/null; then
  rollback
  exit 2
fi

gzip -dc "$work_dir/mad-cpa-codex.tar.gz" | docker load
if ! docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps "$NATIVE_SERVICE"; then
  rollback
  exit 2
fi

healthy=0
for _ in $(seq 1 60); do
  if curl -fsS --max-time 3 "$NATIVE_HEALTH_URL" >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 2
done
if [ "$healthy" -ne 1 ]; then
  rollback
  exit 2
fi

if ! python3 "$NGINX_PATCH"; then
  rollback
  exit 2
fi

# The public gateway must reject anonymous requests before they reach CPA.
if [ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 -X POST \
  -H 'Content-Type: application/json' \
  --data '{"model":"gpt-5.6-sol","input":"health"}' \
  https://mad.myddns.me/codex/v1/responses || true)" != 401 ]; then
  rollback
  exit 2
fi

docker rm -f "$LEGACY_NATIVE_SERVICE" "$LEGACY_COCKPIT_SERVICE" >/dev/null 2>&1 || true
printf '%s\n' "$release_sha" > "$STATE_FILE"
docker image tag "$IMAGE" "mad-cpa-codex:stable-$release_sha"
if [ -n "$old_image_id" ]; then
  old_release_sha=$(cat "$backup_dir/mad-cpa-codex-sha256.txt" 2>/dev/null || true)
  {
    printf 'rollback_tag=%s\n' "$rollback_tag"
    printf 'rollback_release_sha=%s\n' "$old_release_sha"
    printf 'backup_dir=%s\n' "$backup_dir"
  } > "$ROLLBACK_STATE_FILE"
fi
logger -t new-api-autoupdate "native CPA Codex gateway deployed successfully: $release_sha"
