#!/usr/bin/env bash
set -Eeuo pipefail

image='mad-new-api:v3.0.0-production'
expected_image='sha256:ec8a0f6916a1d9cb1fa564e2502dfa5053b09a8ac682e2e9a3573ce6b1cf092e'
container='madapi-v3-solid-ui'
port=13004
root='/opt/madapi-v3-solid-ui'
data_dir="$root/data"
log_dir="$root/logs"
database='/opt/new-api/data/one-api.db'
nginx_site='/etc/nginx/sites-enabled/mad.myddns.me'
script_dir="$(cd "$(dirname "$0")" && pwd)"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="$root/backups/$timestamp"

[[ $(id -u) -eq 0 ]]
[[ -f "$database" && -f "$nginx_site" ]]
[[ "$(docker image inspect -f '{{.Id}}' "$image")" == "$expected_image" ]]
[[ "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:3010/health)" == 200 ]]
[[ "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:3013/health)" == 200 ]]

umask 077
install -d -m 700 "$data_dir" "$log_dir" "$backup"
cp -a "$nginx_site" "$backup/nginx.site.before.conf"
sha256sum \
  /opt/image-url-compat/service.py \
  /opt/image-media-gateway/image-media-gateway \
  /etc/systemd/system/image-url-compat.service \
  /etc/systemd/system/image-media-gateway.service >"$backup/protected.before.sha256"
python3 - "$database" "$data_dir/one-api.db.new" <<'PY'
import sqlite3, sys
with sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True) as src, sqlite3.connect(sys.argv[2]) as dst:
    src.backup(dst)
PY
mv -f "$data_dir/one-api.db.new" "$data_dir/one-api.db"

docker rm -f "$container" >/dev/null 2>&1 || true
docker run -d --name "$container" --restart unless-stopped \
  --memory 256m --cpus 0.5 --pids-limit 128 \
  --health-cmd 'wget -q -O /dev/null http://localhost:3000/api/status || exit 1' \
  --health-interval 30s --health-timeout 10s --health-retries 3 --health-start-period 20s \
  -p "127.0.0.1:$port:3000" \
  -e TZ=Asia/Shanghai -e SQLITE_PATH=/data/one-api.db \
  -v "$data_dir:/data" -v "$log_dir:/app/logs" \
  "$image" --log-dir /app/logs >/dev/null

status=''
for _ in $(seq 1 120); do
  status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:$port/api/status" || true)"
  [[ "$status" == 200 ]] && break
  sleep 0.5
done
[[ "$status" == 200 ]]
for asset in \
  /static/js/async/2692.2b4e459eac.js \
  /static/js/async/7948.965c8ecf10.js \
  /static/js/async/1468.5a670df5f2.js; do
  [[ "$(curl -sSI "http://127.0.0.1:$port$asset" | tr -d '\r' | sed -n 's/^Content-Type: //Ip')" == 'text/javascript; charset=utf-8' ]]
done

candidate="$backup/nginx.site.candidate.conf"
python3 "$script_dir/patch-nginx-v3-solid-ui.py" "$nginx_site" "$candidate"
cp -a "$candidate" "$nginx_site"
restore_nginx() {
  cp -a "$backup/nginx.site.before.conf" "$nginx_site"
  nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true
}
trap restore_nginx ERR
nginx -t
systemctl reload nginx
[[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 https://mad.myddns.me/sign-in)" == 200 ]]
[[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 https://mad.myddns.me/api/status)" == 200 ]]
for asset in \
  /static/js/async/2692.2b4e459eac.js \
  /static/js/async/7948.965c8ecf10.js \
  /static/js/async/1468.5a670df5f2.js; do
  [[ "$(curl -sSI "https://mad.myddns.me$asset" | tr -d '\r' | sed -n 's/^Content-Type: //Ip')" == 'text/javascript; charset=utf-8' ]]
done
(cd / && sha256sum -c "$backup/protected.before.sha256")
trap - ERR
echo "v3_solid_ui_sidecar=ok backup=$backup image=$expected_image status=$status"
