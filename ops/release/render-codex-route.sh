#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <cpa-upstream-url> <new-api-upstream-url> <output-file>" >&2
  exit 64
fi

cpa_upstream=$1
newapi_upstream=$2
output=$3
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
template="$script_dir/nginx-codex-route.conf.template"

case "$cpa_upstream" in
  http://*|https://*) ;;
  *) echo "CPA upstream must start with http:// or https://" >&2; exit 64 ;;
esac

case "$newapi_upstream" in
  http://*|https://*) ;;
  *) echo "NewAPI upstream must start with http:// or https://" >&2; exit 64 ;;
esac

case "$cpa_upstream$newapi_upstream" in
  *[!A-Za-z0-9._:/-]*) echo "upstream contains unsupported characters" >&2; exit 64 ;;
esac

umask 077
sed \
  -e "s|__CPA_UPSTREAM__|$cpa_upstream|g" \
  -e "s|__NEW_API_UPSTREAM__|$newapi_upstream|g" \
  "$template" > "$output"

echo "rendered=$output"
