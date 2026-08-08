#!/bin/sh
set -eu

config_path=/data/config.yaml

cat > "$config_path" <<'EOF'
host: "0.0.0.0"
port: 8317
logging-to-file: false
request-log: false
request-retry: 0
max-retry-credentials: 0
api-keys: []
plugins:
  enabled: false
EOF

exec /CLIProxyAPI/CLIProxyAPI -config "$config_path"
