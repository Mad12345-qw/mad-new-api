#!/bin/sh
set -eu

config_path=${CPA_CONFIG_PATH:-/data/config.yaml}

cat > "$config_path" <<'EOF'
host: "0.0.0.0"
port: 8317
logging-to-file: false
request-log: false
request-retry: 0
max-retry-credentials: 1
disable-cooling: true
api-keys:
  - "internal-api-disabled"
EOF

exec /CLIProxyAPI/CLIProxyAPI -config "$config_path"
