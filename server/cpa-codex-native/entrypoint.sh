#!/bin/sh
set -eu

base_url=${MADAPI_INTERNAL_URL:-http://new-api:3000}
base_url=${base_url%/}
config_path=/data/config.yaml

cat > "$config_path" <<EOF
host: "0.0.0.0"
port: 8317
logging-to-file: false
request-log: false
request-retry: 0
max-retry-credentials: 0
streaming:
  bootstrap-retries: 2
api-keys: []
plugins:
  enabled: true
  dir: "/app/plugins"
  configs:
    madapi-dynamic:
      enabled: true
      base_url: "${base_url}/v1"
      bootstrap_retries: 2
EOF

exec /CLIProxyAPI/CLIProxyAPI -config "$config_path"
