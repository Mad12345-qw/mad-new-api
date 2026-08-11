#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 7 ]]; then
  echo "usage: $0 <clone-dir> <old-image> <candidate-image> <old-port> <candidate-port> <rollback-port> <result-dir>" >&2
  exit 64
fi

clone_dir="$1"
old_image="$2"
candidate_image="$3"
old_port="$4"
candidate_port="$5"
rollback_port="$6"
result_dir="$7"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source_database="$clone_dir/one-api.db"

if [[ ! -s "$source_database" ]]; then
  echo "production clone is missing: $source_database" >&2
  exit 66
fi
if [[ "$result_dir" != "$clone_dir"/* ]]; then
  echo "result-dir must be inside clone-dir" >&2
  exit 64
fi

rm -rf "$result_dir"
mkdir -p "$result_dir/old-data" "$result_dir/candidate-data"
cp --reflink=auto "$source_database" "$result_dir/old-data/one-api.db"
cp --reflink=auto "$source_database" "$result_dir/candidate-data/one-api.db"

container=""
cleanup() {
  if [[ -n "$container" ]]; then
    docker rm -f "$container" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

run_and_wait() {
  local name="$1"
  local image="$2"
  local data_dir="$3"
  local port="$4"
  local log_file="$5"
  local status=""

  container="$name"
  docker rm -f "$container" >/dev/null 2>&1 || true
  docker run --detach --name "$container" \
    --publish "127.0.0.1:$port:3000" \
    --env TZ=Asia/Shanghai \
    --env SESSION_SECRET=madapi-isolated-compatibility-test \
    --volume "$data_dir:/data" \
    "$image" >/dev/null

  for _ in $(seq 1 120); do
    status="$(curl --silent --show-error --max-time 2 \
      --output /dev/null --write-out '%{http_code}' \
      "http://127.0.0.1:$port/api/status" || true)"
    if [[ "$status" =~ ^[234][0-9][0-9]$ ]]; then
      docker logs "$container" >"$log_file" 2>&1
      docker rm -f "$container" >/dev/null
      container=""
      return 0
    fi
    if [[ "$(docker inspect --format '{{.State.Running}}' "$container" 2>/dev/null || true)" != true ]]; then
      break
    fi
    sleep 0.25
  done

  docker logs "$container" >"$log_file" 2>&1 || true
  echo "container did not become healthy: name=$name status=$status" >&2
  return 70
}

compare_critical() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

left = json.load(open(sys.argv[1], "r", encoding="utf-8"))
right = json.load(open(sys.argv[2], "r", encoding="utf-8"))
if left["quick_check"] != "ok" or right["quick_check"] != "ok":
    raise SystemExit("SQLite quick_check is not ok")
if left["critical"] != right["critical"]:
    left_tables = set(left["critical"])
    right_tables = set(right["critical"])
    changed = sorted(
        table
        for table in left_tables | right_tables
        if left["critical"].get(table) != right["critical"].get(table)
    )
    raise SystemExit("critical table fingerprint changed: " + ", ".join(changed))
PY
}

assert_additive_migration() {
  local left_database="$1"
  local right_database="$2"
  local report="$3"
  python3 "$script_dir/sqlite-clone-diff.py" \
    "$left_database" "$right_database" users tokens channels options \
    >"$report"
  python3 - "$report" <<'PY'
import json
import sys

report = json.load(open(sys.argv[1], "r", encoding="utf-8"))
violations = []
for table, result in report.items():
    if result["added_rows"] or result["removed_rows"]:
        violations.append(f"{table}: row set changed")
    if result["changed_by_column"]:
        violations.append(f"{table}: existing values changed")
    if result["removed_columns"]:
        violations.append(f"{table}: columns removed")
if violations:
    raise SystemExit("; ".join(violations))
PY
}

python3 "$script_dir/sqlite-clone-fingerprint.py" "$source_database" \
  >"$result_dir/baseline.json"

run_and_wait madapi-compat-old "$old_image" \
  "$result_dir/old-data" "$old_port" "$result_dir/old.log"
python3 "$script_dir/sqlite-clone-fingerprint.py" \
  "$result_dir/old-data/one-api.db" >"$result_dir/old-after.json"
compare_critical "$result_dir/baseline.json" "$result_dir/old-after.json"

run_and_wait madapi-compat-candidate "$candidate_image" \
  "$result_dir/candidate-data" "$candidate_port" "$result_dir/candidate.log"
python3 "$script_dir/sqlite-clone-fingerprint.py" \
  "$result_dir/candidate-data/one-api.db" >"$result_dir/candidate-after.json"
assert_additive_migration "$source_database" \
  "$result_dir/candidate-data/one-api.db" "$result_dir/candidate-migration.json"

run_and_wait madapi-compat-rollback "$old_image" \
  "$result_dir/candidate-data" "$rollback_port" "$result_dir/rollback.log"
python3 "$script_dir/sqlite-clone-fingerprint.py" \
  "$result_dir/candidate-data/one-api.db" >"$result_dir/rollback-after.json"
compare_critical "$result_dir/candidate-after.json" "$result_dir/rollback-after.json"

baseline_tables="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["table_count"])' "$result_dir/baseline.json")"
candidate_tables="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["table_count"])' "$result_dir/candidate-after.json")"
added_columns="$(python3 -c 'import json,sys; data=json.load(open(sys.argv[1])); print(sum(len(value["added_columns"]) for value in data.values()))' "$result_dir/candidate-migration.json")"

cat >"$result_dir/result.json" <<EOF
{
  "passed": true,
  "old_image": "$old_image",
  "candidate_image": "$candidate_image",
  "old_image_start": "passed",
  "candidate_image_start": "passed",
  "old_image_after_candidate": "passed",
  "critical_data_fingerprints": "unchanged",
  "additive_columns": $added_columns,
  "baseline_tables": $baseline_tables,
  "candidate_tables": $candidate_tables,
  "completed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

cat "$result_dir/result.json"
