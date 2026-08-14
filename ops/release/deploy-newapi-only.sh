#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <release-directory> <git-sha>" >&2
  exit 64
fi

release_dir="$(cd "$1" && pwd)"
git_sha="$2"
script_dir="$(cd "$(dirname "$0")" && pwd)"
container="${MADAPI_NEW_API_CONTAINER:-new-api}"
network="${MADAPI_DOCKER_NETWORK:-new-api_default}"
data_dir="${MADAPI_DATA_DIR:-/opt/new-api/data}"
log_dir="${MADAPI_LOG_DIR:-/opt/new-api/logs}"
database="${MADAPI_SQLITE_DATABASE:-$data_dir/one-api.db}"
public_port="${MADAPI_NEW_API_PORT:-3001}"
candidate_port="${MADAPI_CANDIDATE_NEW_API_PORT:-13001}"
nginx_site="${MADAPI_NGINX_SITE:-/etc/nginx/sites-enabled/mad.myddns.me}"
backup_root="${MADAPI_NEWAPI_ONLY_BACKUP_ROOT:-/opt/madapi-release-backups}"
lock_file="${MADAPI_NEWAPI_ONLY_LOCK_FILE:-/run/lock/madapi-newapi-only-deploy.lock}"
health_attempts="${MADAPI_RELEASE_HEALTH_ATTEMPTS:-120}"
image_compat_health_url="${MADAPI_IMAGE_COMPAT_HEALTH_URL:-http://127.0.0.1:3010/health}"
image_gateway_health_url="${MADAPI_IMAGE_GATEWAY_HEALTH_URL:-http://127.0.0.1:3013/health}"

[[ $(id -u) -eq 0 ]]
[[ "$git_sha" =~ ^[0-9a-f]{40}$ ]]
[[ "$container" =~ ^[A-Za-z0-9._-]+$ ]]
[[ -f "$release_dir/mad-new-api.tar.gz" && -f "$release_dir/release-manifest.json" ]]
[[ -f "$release_dir/frozen-v3-ui-metadata.json" && -f "$release_dir/SHA256SUMS" ]]
[[ -d "$data_dir" && -d "$log_dir" && -f "$database" && -f "$nginx_site" ]]
[[ "$(dirname "$database")" == "$data_dir" ]]
for value in "$public_port" "$candidate_port" "$health_attempts"; do [[ "$value" =~ ^[1-9][0-9]*$ ]]; done
[[ "$public_port" != "$candidate_port" ]]
for command in docker gzip curl nginx sha256sum python3 flock cmp; do command -v "$command" >/dev/null; done

exec 9>"$lock_file"
flock -n 9 || { echo "another NewAPI deployment is active" >&2; exit 75; }

(cd "$release_dir" && sha256sum -c SHA256SUMS)
python3 - "$release_dir/release-manifest.json" "$git_sha" <<'PY'
import json, sys
manifest = json.load(open(sys.argv[1], encoding="utf-8"))
assert manifest["release_format"] == 2
assert manifest["madapi_commit"] == sys.argv[2]
assert manifest["runtime_mode"] == "newapi-native"
assert manifest["cpa_runtime_required"] is False
assert manifest["newapi_image"] == "mad-new-api:latest"
PY

docker inspect "$container" >/dev/null
[[ "$(docker inspect -f '{{.State.Running}}' "$container")" == true ]]
[[ "$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$container")" == "$network" ]]
python3 - "$container" "$data_dir" "$log_dir" <<'PY'
import json, subprocess, sys
container, data_dir, log_dir = sys.argv[1:]
data = json.loads(subprocess.check_output(["docker", "inspect", container], text=True))[0]
mounts = {(item["Source"], item["Destination"]) for item in data["Mounts"]}
assert (data_dir, "/data") in mounts
assert (log_dir, "/app/logs") in mounts
PY

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup_dir="$backup_root/newapi-only-$timestamp"
snapshot_data="$backup_dir/candidate-data"
snapshot_logs="$backup_dir/candidate-logs"
runtime_env="$backup_dir/runtime.env"
candidate="new-api-candidate-$timestamp"
rollback_container="new-api-rollback-$timestamp"
failed_container="new-api-failed-$timestamp"
old_image_id="$(docker inspect -f '{{.Image}}' "$container")"
old_image_ref="$(docker inspect -f '{{.Config.Image}}' "$container")"
old_image_tag="mad-new-api:rollback-$timestamp"
nginx_before="$(sha256sum "$nginx_site" | awk '{print $1}')"
[[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "$image_compat_health_url" || true)" == 200 ]]
[[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "$image_gateway_health_url" || true)" == 200 ]]
nginx -t

umask 077
mkdir -p "$backup_dir/config" "$snapshot_data" "$snapshot_logs"
docker inspect "$container" >"$backup_dir/new-api.inspect.json"
docker ps --format '{{.Names}}|{{.ID}}' | sort >"$backup_dir/containers.before"
docker inspect cpa-codex-native >"$backup_dir/cpa-codex-native.inspect.json" 2>/dev/null || true
cp -a "$nginx_site" "$backup_dir/config/nginx.site.conf"
nginx -T >"$backup_dir/config/nginx.full.conf" 2>"$backup_dir/config/nginx.validation.txt"
protected_paths=(
  /opt/image-url-compat/service.py
  /opt/image-media-gateway/image-media-gateway
  /etc/systemd/system/image-url-compat.service
  /etc/systemd/system/image-media-gateway.service
)
for path in "${protected_paths[@]}" /opt/new-api/docker-compose.yml; do
	[[ ! -f "$path" ]] || cp -a "$path" "$backup_dir/config/$(basename "$path")"
done
for path in "${protected_paths[@]}"; do [[ -f "$path" ]]; done
sha256sum "${protected_paths[@]}" >"$backup_dir/protected-surfaces.before.sha256"
python3 - "$container" "$runtime_env" <<'PY'
import json, subprocess, sys
container, output = sys.argv[1:]
data = json.loads(subprocess.check_output(["docker", "inspect", container], text=True))[0]
with open(output, "w", encoding="utf-8", newline="\n") as handle:
    for item in data["Config"].get("Env") or []:
        if "\n" in item or "\r" in item:
            raise SystemExit("multiline container environment is unsupported")
        name = item.split("=", 1)[0]
        if name.startswith("MADAPI_CPA_"):
            continue
        handle.write(item + "\n")
PY
chmod 600 "$runtime_env"
runtime_guard_args=()
memory_limit="$(docker inspect -f '{{.HostConfig.Memory}}' "$container")"
memory_swap="$(docker inspect -f '{{.HostConfig.MemorySwap}}' "$container")"
nano_cpus="$(docker inspect -f '{{.HostConfig.NanoCpus}}' "$container")"
pids_limit="$(docker inspect -f '{{.HostConfig.PidsLimit}}' "$container")"
oom_score_adj="$(docker inspect -f '{{.HostConfig.OomScoreAdj}}' "$container")"
health_cmd="$(docker inspect -f '{{if .Config.Healthcheck}}{{index .Config.Healthcheck.Test 1}}{{end}}' "$container")"
health_interval="$(docker inspect -f '{{if .Config.Healthcheck}}{{.Config.Healthcheck.Interval}}{{else}}0{{end}}' "$container")"
health_timeout="$(docker inspect -f '{{if .Config.Healthcheck}}{{.Config.Healthcheck.Timeout}}{{else}}0{{end}}' "$container")"
health_start_period="$(docker inspect -f '{{if .Config.Healthcheck}}{{.Config.Healthcheck.StartPeriod}}{{else}}0{{end}}' "$container")"
health_retries="$(docker inspect -f '{{if .Config.Healthcheck}}{{.Config.Healthcheck.Retries}}{{else}}0{{end}}' "$container")"
[[ "$memory_limit" =~ ^[0-9]+$ && "$memory_swap" =~ ^-?[0-9]+$ ]]
[[ "$nano_cpus" =~ ^[0-9]+$ && "$oom_score_adj" =~ ^-?[0-9]+$ ]]
duration_pattern='^[0-9]+(ns|us|ms|s|m|h)?$'
[[ "$health_interval" =~ $duration_pattern && "$health_timeout" =~ $duration_pattern ]]
[[ "$health_start_period" =~ $duration_pattern && "$health_retries" =~ ^[0-9]+$ ]]
[[ "$memory_limit" == 0 ]] || runtime_guard_args+=(--memory "$memory_limit")
[[ "$memory_swap" == 0 ]] || runtime_guard_args+=(--memory-swap "$memory_swap")
if [[ "$nano_cpus" != 0 ]]; then
  cpu_limit="$(python3 - "$nano_cpus" <<'PY'
import decimal, sys
value = decimal.Decimal(sys.argv[1]) / decimal.Decimal(1_000_000_000)
print(format(value.normalize(), 'f'))
PY
)"
  runtime_guard_args+=(--cpus "$cpu_limit")
fi
[[ "$pids_limit" == "<nil>" || "$pids_limit" == 0 ]] || runtime_guard_args+=(--pids-limit "$pids_limit")
runtime_guard_args+=(--oom-score-adj "$oom_score_adj")
if [[ -n "$health_cmd" ]]; then
  runtime_guard_args+=(--health-cmd "$health_cmd")
  [[ "$health_interval" == 0 ]] || runtime_guard_args+=(--health-interval "$health_interval")
  [[ "$health_timeout" == 0 ]] || runtime_guard_args+=(--health-timeout "$health_timeout")
  [[ "$health_start_period" == 0 ]] || runtime_guard_args+=(--health-start-period "$health_start_period")
  [[ "$health_retries" == 0 ]] || runtime_guard_args+=(--health-retries "$health_retries")
fi
python3 - "$database" "$snapshot_data/$(basename "$database")" <<'PY'
import sqlite3, sys
source, target = sys.argv[1:]
with sqlite3.connect(f"file:{source}?mode=ro", uri=True) as src, sqlite3.connect(target) as dst:
    src.backup(dst)
PY
for entry in "$data_dir"/*; do
  [[ -e "$entry" ]] || continue
  case "$(basename "$entry")" in
    "$(basename "$database")"|"$(basename "$database")-wal"|"$(basename "$database")-shm") ;;
    *) cp -a "$entry" "$snapshot_data/" ;;
  esac
done
python3 "$script_dir/sqlite-release-fingerprint.py" "$snapshot_data/$(basename "$database")" "$backup_dir/database.before.json"
docker tag "$old_image_id" "$old_image_tag"
gzip -dc "$release_dir/mad-new-api.tar.gz" | docker load >/dev/null
docker image inspect mad-new-api:latest >/dev/null
candidate_image="mad-new-api:$git_sha"
docker tag mad-new-api:latest "$candidate_image"

cleanup_candidate() {
  docker rm -f "$candidate" >/dev/null 2>&1 || true
}
trap cleanup_candidate EXIT
docker run -d --name "$candidate" --network "$network" --restart no \
  "${runtime_guard_args[@]}" \
  --env-file "$runtime_env" -e NODE_NAME=new-api-release-preflight \
  -p "127.0.0.1:$candidate_port:3000" \
  -v "$snapshot_data:/data" -v "$snapshot_logs:/app/logs" \
  "$candidate_image" --log-dir /app/logs >/dev/null
candidate_status=""
for _ in $(seq 1 "$health_attempts"); do
  candidate_status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:$candidate_port/api/status" || true)"
  [[ "$candidate_status" == 200 ]] && break
  sleep 0.5
done
[[ "$candidate_status" == 200 ]]
python3 "$script_dir/verify-frozen-ui.py" "http://127.0.0.1:$candidate_port" "$release_dir/frozen-v3-ui-metadata.json"
for path in /codex/v1/models /codex/cockpit/v1/models /v1/models; do
  status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "http://127.0.0.1:$candidate_port$path" || true)"
  [[ "$status" =~ ^(200|401|403)$ ]]
done
for asset in /mad-codex/install.ps1 /mad-codex/install.sh /mad-claude/install.ps1 /mad-claude/install.sh; do
  [[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "http://127.0.0.1:$candidate_port$asset")" == 200 ]]
done
docker stop "$candidate" >/dev/null
python3 "$script_dir/sqlite-release-fingerprint.py" "$snapshot_data/$(basename "$database")" "$backup_dir/database.after-candidate.json"
python3 - "$backup_dir/database.before.json" "$backup_dir/database.after-candidate.json" <<'PY'
import json, sys
before, after = (json.load(open(path, encoding="utf-8")) for path in sys.argv[1:])
assert before["protected_counts"] == after["protected_counts"]
PY

# Prove the current production image can reopen the candidate-migrated clone.
docker rm "$candidate" >/dev/null
docker run -d --name "$candidate" --network "$network" --restart no \
  "${runtime_guard_args[@]}" \
  --env-file "$runtime_env" -e NODE_NAME=new-api-rollback-preflight \
  -p "127.0.0.1:$candidate_port:3000" \
  -v "$snapshot_data:/data" -v "$snapshot_logs:/app/logs" \
  "$old_image_tag" --log-dir /app/logs >/dev/null
rollback_status=""
for _ in $(seq 1 "$health_attempts"); do
  rollback_status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:$candidate_port/api/status" || true)"
  [[ "$rollback_status" == 200 ]] && break
  sleep 0.5
done
[[ "$rollback_status" == 200 ]]
docker rm -f "$candidate" >/dev/null
trap - EXIT

# The live cutover changes exactly one container. Nginx and every gateway stay untouched.
cutover_committed=0
restore_live_on_error() {
  local status=$?
  if [[ "$cutover_committed" == 0 ]]; then
    set +e
    if docker inspect "$container" >/dev/null 2>&1; then
      docker stop "$container" >/dev/null 2>&1
      docker rename "$container" "$failed_container" >/dev/null 2>&1
    fi
    if docker inspect "$rollback_container" >/dev/null 2>&1; then
      docker rename "$rollback_container" "$container" >/dev/null 2>&1
      docker start "$container" >/dev/null 2>&1
    fi
    echo "candidate acceptance failed; previous NewAPI was restored" >&2
  fi
  exit "$status"
}
trap restore_live_on_error ERR
docker stop "$container" >/dev/null
docker rename "$container" "$rollback_container"
docker run -d --name "$container" --network "$network" --restart unless-stopped \
  "${runtime_guard_args[@]}" \
  --env-file "$runtime_env" -e NODE_NAME=new-api-native \
  -p "127.0.0.1:$public_port:3000" \
  -v "$data_dir:/data" -v "$log_dir:/app/logs" \
  "$candidate_image" --log-dir /app/logs >/dev/null

live_status=""
for _ in $(seq 1 "$health_attempts"); do
  live_status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:$public_port/api/status" || true)"
  [[ "$live_status" == 200 ]] && break
  sleep 0.5
done
[[ "$live_status" == 200 ]]
[[ "$(docker inspect -f '{{.HostConfig.Memory}}' "$container")" == "$memory_limit" ]]
[[ "$(docker inspect -f '{{.HostConfig.MemorySwap}}' "$container")" == "$memory_swap" ]]
[[ "$(docker inspect -f '{{.HostConfig.NanoCpus}}' "$container")" == "$nano_cpus" ]]
[[ "$(docker inspect -f '{{.HostConfig.PidsLimit}}' "$container")" == "$pids_limit" ]]
[[ "$(docker inspect -f '{{.HostConfig.OomScoreAdj}}' "$container")" == "$oom_score_adj" ]]
if [[ -n "$health_cmd" ]]; then
  [[ "$(docker inspect -f '{{index .Config.Healthcheck.Test 1}}' "$container")" == "$health_cmd" ]]
fi

python3 "$script_dir/verify-frozen-ui.py" "http://127.0.0.1:$public_port" "$release_dir/frozen-v3-ui-metadata.json"
python3 "$script_dir/sqlite-release-fingerprint.py" "$database" "$backup_dir/database.after-live.json"
python3 - "$backup_dir/database.before.json" "$backup_dir/database.after-live.json" <<'PY'
import json, sys
before, after = (json.load(open(path, encoding="utf-8")) for path in sys.argv[1:])
for table in ("channels", "abilities", "options"):
    assert before["protected_counts"][table] == after["protected_counts"][table], table
for table in ("users", "tokens", "logs"):
    assert after["protected_counts"][table] >= before["protected_counts"][table], table
PY
[[ "$(sha256sum "$nginx_site" | awk '{print $1}')" == "$nginx_before" ]]
nginx -T >"$backup_dir/config/nginx.full.after.conf" 2>"$backup_dir/config/nginx.validation.after.txt"
cmp -s "$backup_dir/config/nginx.full.conf" "$backup_dir/config/nginx.full.after.conf"
(cd / && sha256sum -c "$backup_dir/protected-surfaces.before.sha256")
[[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "$image_compat_health_url" || true)" == 200 ]]
[[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "$image_gateway_health_url" || true)" == 200 ]]
nginx -t

docker ps --format '{{.Names}}|{{.ID}}' | sort >"$backup_dir/containers.after"
python3 - "$backup_dir/containers.before" "$backup_dir/containers.after" "$container" "$rollback_container" <<'PY'
import sys
before_path, after_path, changed, rollback = sys.argv[1:]
def load(path):
    return dict(line.rstrip().split("|", 1) for line in open(path, encoding="utf-8") if "|" in line)
before, after = load(before_path), load(after_path)
for name, ident in before.items():
    if name == changed:
        continue
    assert after.get(name) == ident, (name, ident, after.get(name))
assert rollback not in after  # stopped rollback containers are intentionally absent from docker ps
PY

cutover_committed=1
trap - ERR

cat >"$backup_dir/rollback.env" <<EOF
MADAPI_NEWAPI_ONLY_BACKUP=$backup_dir
MADAPI_NEWAPI_ONLY_CURRENT=$container
MADAPI_NEWAPI_ONLY_ROLLBACK=$rollback_container
MADAPI_NEWAPI_ONLY_FAILED=$failed_container
MADAPI_NEWAPI_ONLY_PORT=$public_port
MADAPI_NEWAPI_ONLY_NETWORK=$network
MADAPI_NEWAPI_ONLY_NGINX_SITE=$nginx_site
MADAPI_NEWAPI_ONLY_NGINX_SHA256=$nginx_before
MADAPI_NEWAPI_ONLY_OLD_IMAGE_ID=$old_image_id
MADAPI_NEWAPI_ONLY_OLD_IMAGE_REF=$old_image_ref
MADAPI_NEWAPI_ONLY_NEW_IMAGE=$candidate_image
EOF
chmod 600 "$backup_dir/rollback.env"
find "$backup_dir" -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum >"$backup_dir/SHA256SUMS"
chmod -R go-rwx "$backup_dir"
echo "newapi_only_deploy=ok backup=$backup_dir health=$live_status frozen_ui=verified cpa_runtime_required=false"
