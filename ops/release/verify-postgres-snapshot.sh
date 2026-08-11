#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: $0 <db-container> <db-user> <source-db> <dump-file> <verification-id>" >&2
  exit 64
fi

db_container="$1"
db_user="$2"
source_db="$3"
dump_file="$4"
verification_id="$5"

if [[ ! -s "$dump_file" ]]; then
  echo "dump file is missing or empty: $dump_file" >&2
  exit 66
fi
if [[ ! "$verification_id" =~ ^[A-Za-z0-9_]+$ ]]; then
  echo "invalid verification id" >&2
  exit 64
fi

verify_db="madapi_restore_verify_${verification_id}"
if [[ ! "$verify_db" =~ ^madapi_restore_verify_[A-Za-z0-9_]+$ ]]; then
  echo "unsafe verification database name" >&2
  exit 64
fi

cleanup() {
  docker exec "$db_container" dropdb --username "$db_user" \
    --if-exists "$verify_db" >/dev/null 2>&1 || true
}
trap cleanup EXIT

cleanup
docker exec "$db_container" createdb --username "$db_user" "$verify_db"
docker exec -i "$db_container" pg_restore \
  --username "$db_user" --dbname "$verify_db" \
  --no-owner --no-privileges <"$dump_file"

source_tables="$(docker exec "$db_container" psql --username "$db_user" \
  --dbname "$source_db" --tuples-only --no-align \
  --command "select count(*) from information_schema.tables where table_schema='public' and table_type='BASE TABLE';")"
restored_tables="$(docker exec "$db_container" psql --username "$db_user" \
  --dbname "$verify_db" --tuples-only --no-align \
  --command "select count(*) from information_schema.tables where table_schema='public' and table_type='BASE TABLE';")"

if [[ "$source_tables" -le 0 || "$restored_tables" != "$source_tables" ]]; then
  echo "restore verification failed: source_tables=$source_tables restored_tables=$restored_tables" >&2
  exit 70
fi

echo "restore_verified database=$verify_db tables=$restored_tables"
