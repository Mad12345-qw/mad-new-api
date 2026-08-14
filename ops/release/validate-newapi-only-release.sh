#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$script_dir/../.." && pwd)"
deploy="$script_dir/deploy-newapi-only.sh"
rollback="$script_dir/rollback-newapi-only.sh"
workflow="$root/.github/workflows/build-release.yml"

for path in "$deploy" "$rollback" "$script_dir/verify-frozen-ui.py" "$script_dir/sqlite-release-fingerprint.py"; do
  test -s "$path"
done
bash -n "$deploy"
bash -n "$rollback"
python3 -m py_compile "$script_dir/verify-frozen-ui.py" "$script_dir/sqlite-release-fingerprint.py"
grep -Fq '"cpa_runtime_required": false' "$workflow"
grep -Fq 'deploy-newapi-only.sh' "$workflow"
grep -Fq 'rollback-newapi-only.sh' "$workflow"
! grep -Eq 'docker (build|save).*cpa|mad-cpa-official-gateway.tar.gz|"cpa_image"' "$workflow"
archive_block="$(sed -n '/tar -czf release\/madapi-release-tools.tar.gz/,/ops\/mad-api-error-alert/p' "$workflow")"
! grep -Eq 'deploy-unified|rollback-unified|cpa-upstream|cpa-official' <<<"$archive_block"
grep -Fq 'docker stop "$container"' "$deploy"
grep -Fq 'docker rename "$container" "$rollback_container"' "$deploy"
grep -Fq 'MADAPI_IMAGE_COMPAT_HEALTH_URL:-http://127.0.0.1:3010/health' "$deploy"
grep -Fq 'MADAPI_IMAGE_GATEWAY_HEALTH_URL:-http://127.0.0.1:3013/health' "$deploy"
grep -Fq 'protected-surfaces.before.sha256' "$deploy"
grep -Fq 'name.startswith("MADAPI_CPA_")' "$deploy"
! grep -Eq 'systemctl (stop|restart).*image|nginx -s reload|docker (rm|stop).*cpa' "$deploy"
echo "newapi_only_release_validation=passed"
