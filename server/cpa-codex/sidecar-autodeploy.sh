#!/bin/sh
set -eu

# Keep this release asset name for compatibility with already-installed
# updaters. The front sidecar is retired; this script only reconciles Nginx.
RELEASE_BASE=https://github.com/Mad12345-qw/mad-new-api/releases/download/build-latest
NGINX_PATCH=/usr/local/lib/mad-cpa-codex/patch-nginx.py

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
cache_bust=$(date +%s)

curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
  -o "$work_dir/patch-cpa-codex-nginx.py" "$RELEASE_BASE/patch-cpa-codex-nginx.py?cb=$cache_bust"
curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
  -o "$work_dir/patch-cpa-codex-nginx.py.sha256" "$RELEASE_BASE/patch-cpa-codex-nginx.py.sha256?cb=$cache_bust"

cd "$work_dir"
sha256sum -c patch-cpa-codex-nginx.py.sha256
install -d -m 0755 "$(dirname "$NGINX_PATCH")"
install -m 0755 "$work_dir/patch-cpa-codex-nginx.py" "$NGINX_PATCH"
python3 "$NGINX_PATCH"

logger -t new-api-autoupdate "NewAPI Codex route reconciled successfully"
