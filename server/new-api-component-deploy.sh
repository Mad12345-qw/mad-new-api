#!/bin/sh
set -eu

# Replace only NewAPI while the public Codex route remains on stable CPA.
COMPOSE_DIR=/opt/new-api
COMPOSE_FILE=$COMPOSE_DIR/docker-compose.yml
SERVICE=new-api
IMAGE=mad-new-api:latest
HEALTH_URL=http://127.0.0.1:3001/api/status
RELEASE_BASE=https://github.com/Mad12345-qw/mad-new-api/releases/download/build-latest
LOCK_FILE=/run/lock/mad-new-api-component-deploy.lock

test -f "$COMPOSE_FILE"
exec 9>"$LOCK_FILE"
flock -n 9 || exit 0

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
cache_bust=$(date +%s)
curl -fL --retry 3 --connect-timeout 15 --max-time 900 \
  -o "$work_dir/mad-new-api.tar.gz" "$RELEASE_BASE/mad-new-api.tar.gz?cb=$cache_bust"
curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
  -o "$work_dir/mad-new-api.tar.gz.sha256" "$RELEASE_BASE/mad-new-api.tar.gz.sha256?cb=$cache_bust"
(cd "$work_dir" && sha256sum -c mad-new-api.tar.gz.sha256)

container_id=$(docker compose -f "$COMPOSE_FILE" ps -q "$SERVICE")
test -n "$container_id"
old_image_id=$(docker inspect "$container_id" --format '{{.Image}}')
old_image_name=$(docker inspect "$container_id" --format '{{.Config.Image}}')
timestamp=$(date +%Y%m%d-%H%M%S)
backup_dir="$COMPOSE_DIR/backups/new-api-component-$timestamp"
backup_tag="mad-new-api:rollback-$timestamp"
mkdir -p "$backup_dir"
cp -a "$COMPOSE_FILE" "$backup_dir/docker-compose.yml"
docker image tag "$old_image_id" "$backup_tag"

gzip -dc "$work_dir/mad-new-api.tar.gz" | docker load
python3 -c 'import pathlib,re,sys; p=pathlib.Path(sys.argv[1]); s=p.read_text(); n=re.subn(r"(?m)^(\s+image:\s*).+$", r"\1"+sys.argv[2], s, count=1); assert n[1] == 1; p.write_text(n[0])' "$COMPOSE_FILE" "$IMAGE"

rollback() {
  cp -a "$backup_dir/docker-compose.yml" "$COMPOSE_FILE"
  docker image tag "$backup_tag" "$old_image_name"
  docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps "$SERVICE" || true
}

if ! docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps "$SERVICE"; then
  rollback
  exit 2
fi

for _ in $(seq 1 60); do
  if curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null | grep -q '"success":true'; then
    logger -t mad-new-api-component-deploy "new-api component deployed without touching CPA containers"
    exit 0
  fi
  sleep 2
done

rollback
exit 2
