#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <new-api-upstream-url> <output-file>" >&2
  exit 64
fi

upstream=$1
output=$2
token=${TRUSTED_ROUTE_TOKEN:-}
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
template="$script_dir/nginx-codex-route.conf.template"

case "$upstream" in
  http://*|https://*) ;;
  *) echo "upstream must start with http:// or https://" >&2; exit 64 ;;
esac

case "$upstream" in
  *[!A-Za-z0-9._:/-]*) echo "upstream contains unsupported characters" >&2; exit 64 ;;
esac

case "$token" in
  ''|*[!A-Fa-f0-9]*) echo "TRUSTED_ROUTE_TOKEN must be a non-empty hexadecimal secret" >&2; exit 64 ;;
esac

if [ "${#token}" -lt 64 ]; then
  echo "TRUSTED_ROUTE_TOKEN must contain at least 64 hexadecimal characters" >&2
  exit 64
fi

umask 077
sed \
  -e "s|__NEW_API_UPSTREAM__|$upstream|g" \
  -e "s|__TRUSTED_ROUTE_TOKEN__|$token|g" \
  "$template" > "$output"

echo "rendered=$output"
