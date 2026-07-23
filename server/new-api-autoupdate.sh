#!/bin/sh
set -eu

COMPOSE_DIR=/opt/new-api
SERVICE=new-api
IMAGE=mad-new-api:latest
DB_FILE=/opt/new-api/data/one-api.db
HEALTH_URL=http://127.0.0.1:3001/api/status
LOCK_FILE=/run/lock/new-api-maintenance.lock
RELEASE_BASE=https://github.com/Mad12345-qw/mad-new-api/releases/download/build-latest
STATE_FILE=/opt/new-api/mad-release-sha256.txt
COMPAT_STATE_FILE=/opt/new-api/mad-image-compat-sha256.txt
COMPAT_DIR=/opt/image-url-compat
COMPAT_SCRIPT=$COMPAT_DIR/service.py
COMPAT_UNIT=/etc/systemd/system/image-url-compat.service
COMPAT_HEALTH_URL=http://127.0.0.1:3010/health

exec 9>"$LOCK_FILE"
flock -n 9 || exit 0

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
cache_bust=$(date +%s)

curl -fL --retry 3 --connect-timeout 15 --max-time 900 \
  -o "$work_dir/mad-new-api.tar.gz" "$RELEASE_BASE/mad-new-api.tar.gz?cb=$cache_bust"
curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
  -o "$work_dir/mad-new-api.tar.gz.sha256" "$RELEASE_BASE/mad-new-api.tar.gz.sha256?cb=$cache_bust"
for asset in image-url-compat.py image-url-compat.service patch-image-compat-nginx.py; do
  curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
    -o "$work_dir/$asset" "$RELEASE_BASE/$asset?cb=$cache_bust"
  curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
    -o "$work_dir/$asset.sha256" "$RELEASE_BASE/$asset.sha256?cb=$cache_bust"
done

cd "$work_dir"
sha256sum -c mad-new-api.tar.gz.sha256
sha256sum -c image-url-compat.py.sha256
sha256sum -c image-url-compat.service.sha256
sha256sum -c patch-image-compat-nginx.py.sha256
release_sha=$(sha256sum mad-new-api.tar.gz | awk '{print $1}')
compat_sha=$(cat image-url-compat.py.sha256 image-url-compat.service.sha256 patch-image-compat-nginx.py.sha256 | sha256sum | awk '{print $1}')

if [ ! -f "$COMPAT_STATE_FILE" ] || [ "$(cat "$COMPAT_STATE_FILE")" != "$compat_sha" ]; then
  compat_backup_dir="$COMPOSE_DIR/backups/image-compat-$(date +%Y%m%d-%H%M%S)"
  mkdir -p "$compat_backup_dir"
  [ ! -f "$COMPAT_SCRIPT" ] || cp -a "$COMPAT_SCRIPT" "$compat_backup_dir/service.py"
  [ ! -f "$COMPAT_UNIT" ] || cp -a "$COMPAT_UNIT" "$compat_backup_dir/image-url-compat.service"

  install -d -m 0755 "$COMPAT_DIR"
  install -m 0755 "$work_dir/image-url-compat.py" "$COMPAT_SCRIPT"
  install -m 0644 "$work_dir/image-url-compat.service" "$COMPAT_UNIT"
  systemctl daemon-reload
  systemctl enable image-url-compat.service >/dev/null
  systemctl restart image-url-compat.service

  compat_healthy=0
  for _ in $(seq 1 30); do
    if curl -fsS --max-time 3 "$COMPAT_HEALTH_URL" 2>/dev/null | grep -q '"status":"ok"'; then
      compat_healthy=1
      break
    fi
    sleep 1
  done

  if [ "$compat_healthy" -ne 1 ]; then
    logger -t new-api-autoupdate "image compatibility service health check failed; rolling back"
    [ ! -f "$compat_backup_dir/service.py" ] || cp -a "$compat_backup_dir/service.py" "$COMPAT_SCRIPT"
    [ ! -f "$compat_backup_dir/image-url-compat.service" ] || cp -a "$compat_backup_dir/image-url-compat.service" "$COMPAT_UNIT"
    systemctl daemon-reload
    systemctl restart image-url-compat.service || true
    exit 2
  fi

  python3 "$work_dir/patch-image-compat-nginx.py"

  printf '%s\n' "$compat_sha" > "$COMPAT_STATE_FILE"
  logger -t new-api-autoupdate "image compatibility service updated successfully: $compat_sha"
fi

if [ -f "$STATE_FILE" ] \
  && [ "$(cat "$STATE_FILE")" = "$release_sha" ] \
  && docker image inspect "$IMAGE" >/dev/null 2>&1; then
  logger -t new-api-autoupdate "already current: $release_sha"
  exit 0
fi

container_id=$(docker compose -f "$COMPOSE_DIR/docker-compose.yml" ps -q "$SERVICE")
old_image_id=$(docker inspect "$container_id" --format '{{.Image}}')
old_image_name=$(docker inspect "$container_id" --format '{{.Config.Image}}')

ts=$(date +%Y%m%d-%H%M%S)
backup_dir="$COMPOSE_DIR/backups/release-$ts"
backup_tag="new-api-backup:$ts"
mkdir -p "$backup_dir"
cp -a "$COMPOSE_DIR/docker-compose.yml" "$COMPOSE_DIR/.env" "$backup_dir/"
docker inspect "$container_id" > "$backup_dir/container-inspect.json"
printf '%s\n' "$old_image_id" > "$backup_dir/old-image-id.txt"
python3 -c 'import sqlite3,sys; s=sqlite3.connect(sys.argv[1]); d=sqlite3.connect(sys.argv[2]); s.backup(d); d.close(); s.close()' "$DB_FILE" "$backup_dir/one-api.db"
docker image tag "$old_image_id" "$backup_tag"

gzip -dc "$work_dir/mad-new-api.tar.gz" | docker load
python3 -c 'import pathlib,re,sys; p=pathlib.Path(sys.argv[1]); s=p.read_text(); n=re.subn(r"(?m)^(\s+image:\s*).+$", r"\1"+sys.argv[2], s, count=1); assert n[1] == 1; p.write_text(n[0])' "$COMPOSE_DIR/docker-compose.yml" "$IMAGE"

cd "$COMPOSE_DIR"
docker compose up -d --force-recreate --no-deps "$SERVICE"

healthy=0
for _ in $(seq 1 60); do
  if curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null | grep -q '"success":true'; then
    healthy=1
    break
  fi
  sleep 2
done

if [ "$healthy" -eq 1 ]; then
  printf '%s\n' "$release_sha" > "$STATE_FILE"
  logger -t new-api-autoupdate "release deployed successfully: $release_sha"
  exit 0
fi

logger -t new-api-autoupdate "release health check failed; rolling back"
docker compose stop "$SERVICE" || true
cp -a "$backup_dir/one-api.db" "$DB_FILE"
cp -a "$backup_dir/docker-compose.yml" "$COMPOSE_DIR/docker-compose.yml"
docker image tag "$backup_tag" "$old_image_name"
docker compose up -d --force-recreate --no-deps "$SERVICE"

for _ in $(seq 1 45); do
  if curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null | grep -q '"success":true'; then
    logger -t new-api-autoupdate "rollback succeeded"
    exit 1
  fi
  sleep 2
done

logger -t new-api-autoupdate "rollback failed; manual intervention required"
exit 2
