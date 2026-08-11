#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -lt 8 ]]; then
  echo "usage: $0 <snapshot-root> <release-id> <app-container> <cpa-container> <db-container> <db-user> <db-name> <repo-path> [config-path ...]" >&2
  exit 64
fi

snapshot_root="$1"
release_id="$2"
app_container="$3"
cpa_container="$4"
db_container="$5"
db_user="$6"
db_name="$7"
repo_path="$8"
shift 8

if [[ ! "$release_id" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "invalid release id: $release_id" >&2
  exit 64
fi

snapshot_dir="$snapshot_root/$release_id"
if [[ -e "$snapshot_dir" ]]; then
  echo "snapshot already exists: $snapshot_dir" >&2
  exit 73
fi

umask 077
mkdir -p "$snapshot_dir/config"

manifest="$snapshot_dir/manifest.txt"
{
  printf 'release_id=%s\n' "$release_id"
  printf 'created_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'host=%s\n' "$(hostname)"
  printf 'git_commit=%s\n' "$(git -C "$repo_path" rev-parse HEAD)"
  printf 'app_container=%s\n' "$app_container"
  printf 'app_image_id=%s\n' "$(docker inspect --format '{{.Image}}' "$app_container")"
  printf 'app_image_ref=%s\n' "$(docker inspect --format '{{.Config.Image}}' "$app_container")"
  printf 'cpa_container=%s\n' "$cpa_container"
  printf 'cpa_image_id=%s\n' "$(docker inspect --format '{{.Image}}' "$cpa_container")"
  printf 'cpa_image_ref=%s\n' "$(docker inspect --format '{{.Config.Image}}' "$cpa_container")"
  printf 'database=%s\n' "$db_name"
} >"$manifest"

index=0
for config_path in "$@"; do
  if [[ ! -f "$config_path" ]]; then
    echo "config path does not exist: $config_path" >&2
    exit 66
  fi
  index=$((index + 1))
  destination="$snapshot_dir/config/$(printf '%02d' "$index")-$(basename "$config_path")"
  cp --preserve=mode,timestamps "$config_path" "$destination"
  printf 'config_%02d_source=%s\n' "$index" "$config_path" >>"$manifest"
done

docker exec "$db_container" pg_dump \
  --username "$db_user" --dbname "$db_name" \
  --format=custom --no-owner --no-privileges >"$snapshot_dir/database.dump"

if [[ ! -s "$snapshot_dir/database.dump" ]]; then
  echo "database snapshot is empty" >&2
  exit 74
fi

docker exec -i "$db_container" pg_restore --list \
  <"$snapshot_dir/database.dump" >"$snapshot_dir/database-restore-list.txt"

find "$snapshot_dir" -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum \
  >"$snapshot_dir/SHA256SUMS"
chmod -R go-rwx "$snapshot_dir"

echo "$snapshot_dir"
