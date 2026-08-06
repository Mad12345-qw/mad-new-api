#!/bin/sh
set -eu

# This script only creates or replaces the two isolated canary containers.
# It must never restart new-api, cpa-codex, or cpa-codex-cockpit.
COMPOSE_DIR=/opt/new-api
ENV_FILE=$COMPOSE_DIR/.env
RELEASE_BASE=https://github.com/Mad12345-qw/mad-new-api/releases/download/build-latest
IMAGE=mad-cpa-codex:canary
NATIVE=cpa-codex-canary
COCKPIT=cpa-codex-cockpit-canary
NATIVE_PORT=8328
COCKPIT_PORT=8329
NATIVE_HEALTH=http://127.0.0.1:$NATIVE_PORT/healthz
COCKPIT_HEALTH=http://127.0.0.1:$COCKPIT_PORT/healthz
LOCK_FILE=/run/lock/mad-cpa-codex-canary.lock

test -f "$ENV_FILE"
exec 9>"$LOCK_FILE"
flock -n 9 || exit 0

read_env() {
  key=$1
  sed -n "s/^${key}=//p" "$ENV_FILE" | tail -n 1
}

dispatch_token=$(read_env MADAPI_CODEX_DISPATCH_TOKEN)
catalog_token=$(read_env MADAPI_INTERNAL_CATALOG_TOKEN)
test -n "$dispatch_token"
test -n "$catalog_token"

network=$(docker inspect cpa-codex --format '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}')
test -n "$network"
stable_image_id=$(docker image inspect -f '{{.Id}}' mad-cpa-codex:latest 2>/dev/null || true)

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
cache_bust=$(date +%s)
curl -fL --retry 3 --connect-timeout 15 --max-time 900 \
  -o "$work_dir/mad-cpa-codex.tar.gz" "$RELEASE_BASE/mad-cpa-codex.tar.gz?cb=$cache_bust"
curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
  -o "$work_dir/mad-cpa-codex.tar.gz.sha256" "$RELEASE_BASE/mad-cpa-codex.tar.gz.sha256?cb=$cache_bust"
(cd "$work_dir" && sha256sum -c mad-cpa-codex.tar.gz.sha256)

gzip -dc "$work_dir/mad-cpa-codex.tar.gz" | docker load
loaded_image=$(docker images --format '{{.Repository}}:{{.Tag}}' | awk '$0 == "mad-cpa-codex:latest" { print; exit }')
test "$loaded_image" = "mad-cpa-codex:latest"
docker image tag mad-cpa-codex:latest "$IMAGE"
if [ -n "$stable_image_id" ]; then
  docker image tag "$stable_image_id" mad-cpa-codex:latest
fi

run_candidate() {
  name=$1
  port=$2
  mode=$3
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker run -d --name "$name" --restart unless-stopped --memory 512m \
    --network "$network" -p "127.0.0.1:$port:8317" \
    -v "$COMPOSE_DIR/cpa-codex:/data:ro" \
    -e TZ=Asia/Shanghai \
    -e MADAPI_CODEX_DISPATCH_TOKEN="$dispatch_token" \
    -e MADAPI_INTERNAL_URL=http://new-api:3000 \
    -e MADAPI_INTERNAL_CATALOG_TOKEN="$catalog_token" \
    -e CPA_CATALOG_MODE="$mode" \
    -e CPA_CATALOG_READ_ONLY=1 \
    -e CPA_CONFIG_PATH="/data/$mode-config.yaml" \
    "$IMAGE" >/dev/null
}

run_candidate "$NATIVE" "$NATIVE_PORT" native
run_candidate "$COCKPIT" "$COCKPIT_PORT" cockpit

for health_url in "$NATIVE_HEALTH" "$COCKPIT_HEALTH"; do
  healthy=0
  for _ in $(seq 1 60); do
    if curl -fsS --max-time 3 "$health_url" >/dev/null 2>&1; then
      healthy=1
      break
    fi
    sleep 1
  done
  if [ "$healthy" -ne 1 ]; then
    docker logs --tail 80 "$NATIVE" >&2 || true
    docker logs --tail 80 "$COCKPIT" >&2 || true
    exit 2
  fi
done

logger -t mad-cpa-codex-canary "candidate CPA containers are healthy"
