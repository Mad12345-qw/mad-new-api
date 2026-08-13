#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
compose="$script_dir/docker-compose.unified.yml"
route="$script_dir/nginx-unified-route.conf.template"
legacy="$script_dir/deploy-codex-control-only.sh"

command -v python3 >/dev/null
command -v bash >/dev/null
test -f "$compose"
test -f "$route"
test -f "$legacy"

for file in "$script_dir"/*.sh; do
  bash -n "$file"
done

python3 - "$compose" "$route" "$legacy" <<'PY'
from pathlib import Path
import sys

compose, route, legacy = [Path(value).read_text(encoding="utf-8") for value in sys.argv[1:]]
for marker in (
    "  new-api:\n",
    "  cpa-official-gateway:\n",
    "MADAPI_NEWAPI_CONTROL_URL: http://new-api:3000/internal/madapi/cpa",
    "MADAPI_CPA_HANDLER_URL: http://cpa-official-gateway:18417/execute",
    "name: ${MADAPI_DOCKER_NETWORK:-new-api_default}",
):
    if marker.replace("$", "$") not in compose:
        raise SystemExit(f"unified compose is missing: {marker}")
if "new-api-codex-control" in compose:
    raise SystemExit("unified compose still contains the retired second NewAPI")

deploy = (Path(sys.argv[1]).parent / "deploy-unified-newapi.sh").read_text(encoding="utf-8")
rollback = (Path(sys.argv[1]).parent / "rollback-unified-newapi.sh").read_text(encoding="utf-8")
for marker in (
    'database="${MADAPI_SQLITE_DATABASE:-$data_dir/one-api.db}"',
    "source.backup(target)",
    'docker stop "${old_writers[@]}"',
    'candidate_new="new-api"',
    'candidate_cpa="cpa-official-gateway"',
    'manifest_commit="$(python3',
    'docker tag mad-new-api:latest "$new_api_image"',
    'docker tag mad-cpa-official-gateway:latest "$cpa_image"',
    "sqlite_single_writer=true",
    "check_pair",
    'start_pair "$snapshot_data" "$snapshot_logs" "$candidate_port"',
    'start_pair "$data_dir" "$log_dir" "$old_port"',
    "candidate-data",
    'image_gateway_release_binary="$release_dir/image-media-gateway"',
    'cp -a "$image_gateway_binary" "$backup_dir/image-gateway-binary.before"',
    'cp -a "$image_gateway_release_binary" "$backup_dir/image-gateway-binary.candidate"',
    'mv -f "$image_gateway_binary.tmp" "$image_gateway_binary"',
    'image-gateway-upstream.before.conf',
    'image-gateway-upstream.candidate.conf',
    'systemctl restart "$image_gateway_service"',
    'image_compat_port="${MADAPI_IMAGE_COMPAT_PORT:-3010}"',
    'UPSTREAM=http://127.0.0.1:$image_compat_port',
    'systemctl is-active --quiet image-url-compat.service',
    '"http://127.0.0.1:$image_port/health"',
):
    if marker not in deploy:
        raise SystemExit(f"SQLite single-writer deploy gate is missing: {marker}")
if 'UPSTREAM=http://127.0.0.1:$old_port' in deploy:
    raise SystemExit("image gateway bypasses the complete image compatibility service")
for marker in ("docker stop \"$candidate_new\" \"$candidate_cpa\"", 'docker rename "$candidate_new" "$deployed_new_backup"', 'docker rename "$deployed_new_backup" "$candidate_new"', "cpa_status", "sqlite_single_writer=true", "image-gateway-upstream.before.conf", "image-gateway-upstream.candidate.conf", "image-gateway-binary.before", "image-gateway-binary.candidate", 'systemctl restart "$image_gateway_service"'):
    if marker not in rollback:
        raise SystemExit(f"SQLite single-writer rollback gate is missing: {marker}")

for marker in (
    "location ^~ /v1/",
    "proxy_pass http://127.0.0.1:__NEW_API_PORT__;",
    "location ^~ /codex/v1/",
    "location = /v1/images/generations",
    "location = /v1/images/edits",
    "proxy_pass http://127.0.0.1:__IMAGE_PORT__;",
):
    if marker not in route:
        raise SystemExit(f"unified nginx template is missing: {marker}")
if "new-api-codex-control" in route:
    raise SystemExit("unified nginx template references the retired second NewAPI")
if "new-api-codex-control" not in legacy:
    raise SystemExit("legacy rollback deployment definition was not preserved")
print("unified_orchestration_static_checks=passed")
PY

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
bash "$script_dir/render-unified-route.sh" 3001 3013 "$tmp_dir/route.conf"
grep -Fq 'proxy_pass http://127.0.0.1:3001;' "$tmp_dir/route.conf"
grep -Fq 'proxy_pass http://127.0.0.1:3013;' "$tmp_dir/route.conf"
test "$(grep -Fc 'proxy_pass http://127.0.0.1:3013;' "$tmp_dir/route.conf")" -eq 2
! grep -Fq 'new-api-codex-control' "$tmp_dir/route.conf"
echo "unified_orchestration_validation=passed"
