#!/bin/sh
set -eu

config_path=${CPA_CONFIG_PATH:-/data/config.yaml}

if [ "${CPA_CATALOG_READ_ONLY:-0}" = 1 ]; then
  test -s "$config_path"
else
  if ! /usr/local/bin/catalog-sync.py; then
    if [ ! -s "$config_path" ]; then
      echo "MadAPI CPA has no usable model catalog" >&2
      exit 1
    fi
    echo "MadAPI CPA is using its last validated model catalog" >&2
  fi

  (
    while :; do
      sleep 10800
      /usr/local/bin/catalog-sync.py || echo "MadAPI CPA catalog refresh failed; retaining the current catalog" >&2
    done
  ) &
fi

exec /CLIProxyAPI/CLIProxyAPI -config "$config_path"
