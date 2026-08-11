#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 4 ]]; then
  echo "usage: $0 <container-name> <new-image> <host-port> <network>" >&2
  exit 64
fi

container_name="$1"
new_image="$2"
host_port="$3"
network="$4"
saved_name="${container_name}-saved-$(date -u +%Y%m%dT%H%M%SZ)"
env_file="$(mktemp)"
chmod 600 "$env_file"
trap 'rm -f "$env_file"' EXIT

docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' \
  "$container_name" >"$env_file"
old_image="$(docker inspect --format '{{.Config.Image}}' "$container_name")"

docker stop "$container_name" >/dev/null
docker rename "$container_name" "$saved_name"

restore_old() {
  docker rm -f "$container_name" >/dev/null 2>&1 || true
  docker rename "$saved_name" "$container_name"
  docker start "$container_name" >/dev/null
}

if ! docker run --detach --name "$container_name" \
    --network "$network" \
    --publish "127.0.0.1:$host_port:3000" \
    --env-file "$env_file" \
    --restart unless-stopped \
    "$new_image" >/dev/null; then
  restore_old
  echo "candidate start failed; old test container restored" >&2
  exit 70
fi

status=""
for _ in $(seq 1 120); do
  status="$(curl --silent --max-time 2 --output /dev/null \
    --write-out '%{http_code}' "http://127.0.0.1:$host_port/api/status" || true)"
  if [[ "$status" =~ ^[234][0-9][0-9]$ ]]; then
    echo "active=$new_image saved_container=$saved_name saved_image=$old_image"
    exit 0
  fi
  if [[ "$(docker inspect --format '{{.State.Running}}' "$container_name" 2>/dev/null || true)" != true ]]; then
    break
  fi
  sleep 0.25
done

docker logs "$container_name" >&2 || true
restore_old
echo "candidate health failed; old test container restored status=$status" >&2
exit 69
