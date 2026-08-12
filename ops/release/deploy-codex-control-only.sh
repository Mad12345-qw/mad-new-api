#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <release-directory> <git-sha>" >&2
  exit 64
fi

release_dir=$1
git_sha=$2
site=/etc/nginx/sites-enabled/mad.myddns.me
network=new-api_default

[[ $(id -u) -eq 0 ]]
[[ $git_sha =~ ^[0-9a-f]{40}$ ]]
cd "$release_dir"
sha256sum -c SHA256SUMS

assert_v1_boundary() {
  nginx -T 2>/dev/null | grep -Fq 'location ^~ /v1/'
  nginx -T 2>/dev/null | grep -Fq 'proxy_pass http://127.0.0.1:3001;'
  nginx -T 2>/dev/null | grep -Fq 'location = /v1/images/generations'
  nginx -T 2>/dev/null | grep -Fq 'proxy_pass http://127.0.0.1:3013;'
}

assert_v1_boundary
docker image inspect mad-new-api:latest >/dev/null
v1_image_before=$(docker inspect -f '{{.Image}}' new-api)

gzip -dc mad-new-api-codex-control.tar.gz | docker load
gzip -dc mad-cpa-official-gateway.tar.gz | docker load

docker rm -f new-api-codex-control cpa-official-gateway >/dev/null 2>&1 || true
docker run -d \
  --name new-api-codex-control \
  --restart unless-stopped \
  --network "$network" \
  --network-alias new-api-codex-control \
  --memory 768m --memory-swap 1024m --cpus 1.25 --pids-limit 256 \
  -p 127.0.0.1:3002:3000 \
  --env-file /opt/madapi-releases/7c90de45/production-runtime/production.env \
  -e TZ=Asia/Shanghai -e ERROR_LOG_ENABLED=true -e BATCH_UPDATE_ENABLED=true \
  -e NODE_NAME=new-api-codex-control -e MADAPI_GEMINI_IMAGE_CONCURRENCY=1 \
  -e GOMEMLIMIT=512MiB -e GOGC=50 \
  -v /opt/new-api/data:/data -v /opt/new-api/logs:/app/logs \
  "mad-new-api-codex-control:$git_sha" --log-dir /app/logs >/dev/null

status=''
for _ in $(seq 1 120); do
  status=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 http://127.0.0.1:3002/api/status || true)
  [[ $status == 200 ]] && break
  sleep 0.5
done
[[ $status == 200 ]]

docker run -d \
  --name cpa-official-gateway \
  --restart unless-stopped \
  --network "$network" \
  --memory 256m --memory-swap 384m --cpus 0.75 --pids-limit 128 \
  -p 127.0.0.1:8330:18317 \
  --env-file /opt/madapi-releases/7c90de45/production-runtime/production.env \
  -e MADAPI_NEWAPI_CONTROL_URL=http://new-api-codex-control:3000/internal/madapi/cpa \
  "mad-cpa-official-gateway:$git_sha" >/dev/null

python3 - "$site" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
text = path.read_text(encoding="utf-8")

def replace_location(source, prefix, block):
    start = source.find(prefix)
    if start < 0:
        raise SystemExit("required nginx location is missing: " + prefix)
    brace = source.find("{", start)
    depth = 0
    for index in range(brace, len(source)):
        if source[index] == "{":
            depth += 1
        elif source[index] == "}":
            depth -= 1
            if depth == 0:
                return source[:start] + block + source[index + 1:]
    raise SystemExit("malformed nginx location: " + prefix)

codex = """location ^~ /codex/v1/ {
        rewrite ^/codex/v1/(.*)$ /v1/$1 break;
        proxy_pass http://127.0.0.1:8330;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }"""
text = replace_location(text, "location ^~ /codex/v1/ {", codex)

assets = """location ^~ /mad-codex/ {
        proxy_pass http://127.0.0.1:3002;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }"""
if "location ^~ /mad-codex/ {" in text:
    text = replace_location(text, "location ^~ /mad-codex/ {", assets)
else:
    marker = text.find("location ^~ /codex/v1/ {")
    text = text[:marker] + assets + "\n\n    " + text[marker:]
path.write_text(text, encoding="utf-8")
PY

nginx -t
systemctl reload nginx
assert_v1_boundary
[[ $(docker inspect -f '{{.Image}}' new-api) == "$v1_image_before" ]]
curl -fsS --max-time 15 https://mad.myddns.me/api/status >/dev/null
curl -fsS --max-time 15 https://mad.myddns.me/mad-codex/install.ps1 >/dev/null
printf 'CODEX_CONTROL_DEPLOYED=%s\n' "$git_sha"
