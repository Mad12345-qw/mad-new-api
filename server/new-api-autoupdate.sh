#!/bin/sh
set -eu

COMPOSE_DIR=/opt/new-api
SERVICE=new-api
IMAGE=mad-new-api:latest
DB_FILE=/opt/new-api/data/one-api.db
HEALTH_URL=http://127.0.0.1:3001/api/status
LOCK_FILE=/run/lock/new-api-maintenance.lock
RELEASE_BASE=https://github.com/Mad12345-qw/mad-new-api/releases/download/build-latest
STATE_FILE=/opt/new-api/mad-release-sha256.txt
COMPAT_STATE_FILE=/opt/new-api/mad-image-compat-sha256.txt
COMPAT_DIR=/opt/image-url-compat
COMPAT_SCRIPT=$COMPAT_DIR/service.py
COMPAT_UNIT=/etc/systemd/system/image-url-compat.service
COMPAT_HEALTH_URL=http://127.0.0.1:3010/health
IMAGE_GATEWAY_STATE_FILE=/opt/new-api/mad-image-gateway-sha256.txt
IMAGE_GATEWAY_DIR=/opt/image-media-gateway
IMAGE_GATEWAY_BIN=$IMAGE_GATEWAY_DIR/image-media-gateway
IMAGE_GATEWAY_UNIT=/etc/systemd/system/image-media-gateway.service
IMAGE_GATEWAY_HEALTH_URL=http://127.0.0.1:3012/health
HOME_DIR=/opt/mad-home
HOME_STATE_FILE=/opt/new-api/mad-home-sha256.txt
SELF_SCRIPT=/usr/local/sbin/new-api-autoupdate.sh
CPA_DEPLOY_SCRIPT=/usr/local/sbin/cpa-codex-autodeploy.sh
ALERT_SCRIPT=/usr/local/sbin/mad-api-error-alert.py
ALERT_SERVICE=/etc/systemd/system/mad-api-error-alert.service
ALERT_TIMER=/etc/systemd/system/mad-api-error-alert.timer
ALERT_STATE_FILE=/opt/new-api/mad-api-error-alert-sha256.txt

exec 9>"$LOCK_FILE"
flock -n 9 || exit 0

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
cache_bust=$(date +%s)

for asset in mad-home.tar.gz new-api-autoupdate.sh cpa-codex-autodeploy.sh mad-api-error-alert.py mad-api-error-alert.service mad-api-error-alert.timer; do
  curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
    -o "$work_dir/$asset" "$RELEASE_BASE/$asset?cb=$cache_bust"
  curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
    -o "$work_dir/$asset.sha256" "$RELEASE_BASE/$asset.sha256?cb=$cache_bust"
done

cd "$work_dir"
sha256sum -c mad-home.tar.gz.sha256
sha256sum -c new-api-autoupdate.sh.sha256
sha256sum -c cpa-codex-autodeploy.sh.sha256
sha256sum -c mad-api-error-alert.py.sha256
sha256sum -c mad-api-error-alert.service.sha256
sha256sum -c mad-api-error-alert.timer.sha256
home_sha=$(awk '{print $1}' mad-home.tar.gz.sha256)
self_sha=$(awk '{print $1}' new-api-autoupdate.sh.sha256)
cpa_deploy_sha=$(awk '{print $1}' cpa-codex-autodeploy.sh.sha256)
alert_sha=$(cat mad-api-error-alert.py.sha256 mad-api-error-alert.service.sha256 mad-api-error-alert.timer.sha256 | sha256sum | awk '{print $1}')

install_updater() {
  current_sha=''
  if [ -f "$SELF_SCRIPT" ]; then
    current_sha=$(sha256sum "$SELF_SCRIPT" | awk '{print $1}')
  fi
  if [ "$current_sha" != "$self_sha" ]; then
    [ ! -f "$SELF_SCRIPT" ] || cp -a "$SELF_SCRIPT" "$SELF_SCRIPT.bak"
    install -m 0755 "$work_dir/new-api-autoupdate.sh" "$SELF_SCRIPT"
    logger -t new-api-autoupdate "updater refreshed successfully: $self_sha"
  fi
}

install_cpa_deployer() {
  current_sha=''
  if [ -f "$CPA_DEPLOY_SCRIPT" ]; then
    current_sha=$(sha256sum "$CPA_DEPLOY_SCRIPT" | awk '{print $1}')
  fi
  if [ "$current_sha" != "$cpa_deploy_sha" ]; then
    [ ! -f "$CPA_DEPLOY_SCRIPT" ] || cp -a "$CPA_DEPLOY_SCRIPT" "$CPA_DEPLOY_SCRIPT.bak"
    install -m 0755 "$work_dir/cpa-codex-autodeploy.sh" "$CPA_DEPLOY_SCRIPT"
    logger -t new-api-autoupdate "CPA Codex deployer refreshed successfully: $cpa_deploy_sha"
  fi
}

install_error_alert() {
  current_sha=''
  if [ -f "$ALERT_STATE_FILE" ]; then
    current_sha=$(cat "$ALERT_STATE_FILE")
  fi
  if [ "$current_sha" = "$alert_sha" ]; then
    return
  fi

  ts=$(date +%Y%m%d-%H%M%S)
  backup_dir="$COMPOSE_DIR/backups/mad-api-error-alert-$ts"
  mkdir -p "$backup_dir"
  for path in "$ALERT_SCRIPT" "$ALERT_SERVICE" "$ALERT_TIMER"; do
    [ ! -f "$path" ] || cp -a "$path" "$backup_dir/"
  done

  install -m 0755 "$work_dir/mad-api-error-alert.py" "$ALERT_SCRIPT"
  install -m 0644 "$work_dir/mad-api-error-alert.service" "$ALERT_SERVICE"
  install -m 0644 "$work_dir/mad-api-error-alert.timer" "$ALERT_TIMER"
  systemctl daemon-reload
  if ! systemctl enable mad-api-error-alert.timer >/dev/null || ! systemctl restart mad-api-error-alert.timer; then
    logger -t new-api-autoupdate "error alert installation failed; rolling back"
    for path in "$ALERT_SCRIPT" "$ALERT_SERVICE" "$ALERT_TIMER"; do
      name=$(basename "$path")
      if [ -f "$backup_dir/$name" ]; then
        cp -a "$backup_dir/$name" "$path"
      else
        rm -f "$path"
      fi
    done
    systemctl daemon-reload
    systemctl restart mad-api-error-alert.timer || true
    exit 2
  fi
  printf '%s\n' "$alert_sha" > "$ALERT_STATE_FILE"
  logger -t new-api-autoupdate "error alert monitor updated successfully: $alert_sha"
}

if [ ! -f "$HOME_STATE_FILE" ] || [ "$(cat "$HOME_STATE_FILE")" != "$home_sha" ]; then
  ts=$(date +%Y%m%d-%H%M%S)
  home_stage=/opt/mad-home-stage-$ts
  home_backup_dir=$COMPOSE_DIR/backups/mad-home-$ts
  rm -rf "$home_stage"
  mkdir -p "$home_backup_dir"

  if [ -d "$HOME_DIR" ]; then
    cp -a "$HOME_DIR" "$home_stage"
  else
    mkdir -p "$home_stage"
  fi

  tar --no-same-owner -xzf "$work_dir/mad-home.tar.gz" -C "$home_stage"
  test -s "$home_stage/index.html"
  test -s "$home_stage/assets/mad-logo.svg"
  grep -Fq 'https://mad.myddns.me/codex/v1' "$home_stage/index.html"

  if [ -d "$HOME_DIR" ]; then
    mv "$HOME_DIR" "$home_backup_dir/mad-home"
  fi
  if mv "$home_stage" "$HOME_DIR"; then
    printf '%s\n' "$home_sha" > "$HOME_STATE_FILE"
    logger -t new-api-autoupdate "homepage updated successfully: $home_sha"
  else
    [ ! -d "$home_backup_dir/mad-home" ] || mv "$home_backup_dir/mad-home" "$HOME_DIR"
    logger -t new-api-autoupdate "homepage update failed; rolled back"
    exit 2
  fi
fi

if [ "${MAD_HOME_ONLY:-0}" = 1 ]; then
  install_updater
  install_cpa_deployer
  install_error_alert
  logger -t new-api-autoupdate "homepage-only release completed: $home_sha"
  exit 0
fi

curl -fL --retry 3 --connect-timeout 15 --max-time 900 \
  -o "$work_dir/mad-new-api.tar.gz" "$RELEASE_BASE/mad-new-api.tar.gz?cb=$cache_bust"
curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
  -o "$work_dir/mad-new-api.tar.gz.sha256" "$RELEASE_BASE/mad-new-api.tar.gz.sha256?cb=$cache_bust"
for asset in image-url-compat.py image-url-compat.service patch-image-compat-nginx.py image-media-gateway image-media-gateway.service; do
  curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
    -o "$work_dir/$asset" "$RELEASE_BASE/$asset?cb=$cache_bust"
  curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
    -o "$work_dir/$asset.sha256" "$RELEASE_BASE/$asset.sha256?cb=$cache_bust"
done

cd "$work_dir"
sha256sum -c mad-new-api.tar.gz.sha256
sha256sum -c image-url-compat.py.sha256
sha256sum -c image-url-compat.service.sha256
sha256sum -c patch-image-compat-nginx.py.sha256
sha256sum -c image-media-gateway.sha256
sha256sum -c image-media-gateway.service.sha256
release_sha=$(sha256sum mad-new-api.tar.gz | awk '{print $1}')
compat_sha=$(cat image-url-compat.py.sha256 image-url-compat.service.sha256 patch-image-compat-nginx.py.sha256 | sha256sum | awk '{print $1}')
expected_compat_source_sha=$(awk '{print $1}' image-url-compat.py.sha256)
image_gateway_sha=$(cat image-media-gateway.sha256 image-media-gateway.service.sha256 | sha256sum | awk '{print $1}')
running_compat_source_sha=$(curl -fsS --max-time 3 "$COMPAT_HEALTH_URL" 2>/dev/null \
  | python3 -c 'import json,sys; print(json.load(sys.stdin).get("source_sha256", ""))' 2>/dev/null || true)

if [ ! -f "$COMPAT_STATE_FILE" ] \
  || [ "$(cat "$COMPAT_STATE_FILE")" != "$compat_sha" ] \
  || [ "$running_compat_source_sha" != "$expected_compat_source_sha" ]; then
  compat_backup_dir="$COMPOSE_DIR/backups/image-compat-$(date +%Y%m%d-%H%M%S)"
  mkdir -p "$compat_backup_dir"
  [ ! -f "$COMPAT_SCRIPT" ] || cp -a "$COMPAT_SCRIPT" "$compat_backup_dir/service.py"
  [ ! -f "$COMPAT_UNIT" ] || cp -a "$COMPAT_UNIT" "$compat_backup_dir/image-url-compat.service"

  install -d -m 0755 "$COMPAT_DIR"
  install -m 0755 "$work_dir/image-url-compat.py" "$COMPAT_SCRIPT"
  install -m 0644 "$work_dir/image-url-compat.service" "$COMPAT_UNIT"
  systemctl daemon-reload
  systemctl enable image-url-compat.service >/dev/null
  systemctl restart image-url-compat.service

  compat_healthy=0
  for _ in $(seq 1 30); do
    running_compat_source_sha=$(curl -fsS --max-time 3 "$COMPAT_HEALTH_URL" 2>/dev/null \
      | python3 -c 'import json,sys; data=json.load(sys.stdin); print(data.get("source_sha256", "") if data.get("status") == "ok" else "")' 2>/dev/null || true)
    if [ "$running_compat_source_sha" = "$expected_compat_source_sha" ]; then
      compat_healthy=1
      break
    fi
    sleep 1
  done

  if [ "$compat_healthy" -ne 1 ]; then
    logger -t new-api-autoupdate "image compatibility service health check failed; rolling back"
    [ ! -f "$compat_backup_dir/service.py" ] || cp -a "$compat_backup_dir/service.py" "$COMPAT_SCRIPT"
    [ ! -f "$compat_backup_dir/image-url-compat.service" ] || cp -a "$compat_backup_dir/image-url-compat.service" "$COMPAT_UNIT"
    systemctl daemon-reload
    systemctl restart image-url-compat.service || true
    exit 2
  fi

  printf '%s\n' "$compat_sha" > "$COMPAT_STATE_FILE"
  logger -t new-api-autoupdate "image compatibility service updated successfully: $compat_sha"
fi

gateway_backup_dir=''
gateway_had_binary=0
gateway_had_unit=0

rollback_image_gateway() {
  [ -n "$gateway_backup_dir" ] || return 0
  if [ "$gateway_had_binary" -eq 1 ]; then
    cp -a "$gateway_backup_dir/image-media-gateway" "$IMAGE_GATEWAY_BIN"
  else
    rm -f "$IMAGE_GATEWAY_BIN"
  fi
  if [ "$gateway_had_unit" -eq 1 ]; then
    cp -a "$gateway_backup_dir/image-media-gateway.service" "$IMAGE_GATEWAY_UNIT"
  else
    rm -f "$IMAGE_GATEWAY_UNIT"
  fi
  systemctl daemon-reload
  if [ "$gateway_had_binary" -eq 1 ] && [ "$gateway_had_unit" -eq 1 ]; then
    systemctl restart image-media-gateway.service || true
  else
    systemctl disable --now image-media-gateway.service >/dev/null 2>&1 || true
  fi
}

running_gateway_sha=''
if [ -f "$IMAGE_GATEWAY_BIN" ] && [ -f "$IMAGE_GATEWAY_UNIT" ]; then
  gateway_binary_sha=$(sha256sum "$IMAGE_GATEWAY_BIN" | awk '{print $1}')
  gateway_unit_sha=$(sha256sum "$IMAGE_GATEWAY_UNIT" | awk '{print $1}')
  running_gateway_sha=$(printf '%s  image-media-gateway\n%s  image-media-gateway.service\n' \
    "$gateway_binary_sha" "$gateway_unit_sha" | sha256sum | awk '{print $1}')
fi

if [ ! -f "$IMAGE_GATEWAY_STATE_FILE" ] \
  || [ "$(cat "$IMAGE_GATEWAY_STATE_FILE")" != "$image_gateway_sha" ] \
  || [ "$running_gateway_sha" != "$image_gateway_sha" ]; then
  gateway_backup_dir="$COMPOSE_DIR/backups/image-gateway-$(date +%Y%m%d-%H%M%S)"
  mkdir -p "$gateway_backup_dir"
  if [ -f "$IMAGE_GATEWAY_BIN" ]; then
    cp -a "$IMAGE_GATEWAY_BIN" "$gateway_backup_dir/image-media-gateway"
    gateway_had_binary=1
  fi
  if [ -f "$IMAGE_GATEWAY_UNIT" ]; then
    cp -a "$IMAGE_GATEWAY_UNIT" "$gateway_backup_dir/image-media-gateway.service"
    gateway_had_unit=1
  fi

  if ! id -u imagecompat >/dev/null 2>&1; then
    useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin --gid www-data imagecompat
  fi
  install -d -m 0755 "$IMAGE_GATEWAY_DIR"
  install -d -o imagecompat -g www-data -m 0750 "$IMAGE_GATEWAY_DIR/spool" /opt/image-url-cache
  install -m 0755 "$work_dir/image-media-gateway" "$IMAGE_GATEWAY_BIN"
  install -m 0644 "$work_dir/image-media-gateway.service" "$IMAGE_GATEWAY_UNIT"
  systemctl daemon-reload
  systemctl enable image-media-gateway.service >/dev/null
  systemctl restart image-media-gateway.service

  gateway_healthy=0
  for _ in $(seq 1 30); do
    if curl -fsS --max-time 3 "$IMAGE_GATEWAY_HEALTH_URL" 2>/dev/null | grep -q '"ok":true'; then
      gateway_healthy=1
      break
    fi
    sleep 1
  done
  if [ "$gateway_healthy" -ne 1 ]; then
    logger -t new-api-autoupdate "streaming image gateway health check failed; rolling back"
    rollback_image_gateway
    exit 2
  fi
fi

activate_image_gateway() {
  if ! python3 "$work_dir/patch-image-compat-nginx.py"; then
    logger -t new-api-autoupdate "streaming image gateway route activation failed"
    return 1
  fi
  printf '%s\n' "$image_gateway_sha" > "$IMAGE_GATEWAY_STATE_FILE"
  logger -t new-api-autoupdate "streaming image gateway activated successfully: $image_gateway_sha"
}

if [ -f "$STATE_FILE" ] \
  && [ "$(cat "$STATE_FILE")" = "$release_sha" ] \
  && docker image inspect "$IMAGE" >/dev/null 2>&1; then
  install_updater
  install_cpa_deployer
  install_error_alert
  if ! activate_image_gateway; then
    rollback_image_gateway
    exit 2
  fi
  logger -t new-api-autoupdate "already current: $release_sha; CPA containers left untouched"
  exit 0
fi

container_id=$(docker compose -f "$COMPOSE_DIR/docker-compose.yml" ps -q "$SERVICE")
old_image_id=$(docker inspect "$container_id" --format '{{.Image}}')
old_image_name=$(docker inspect "$container_id" --format '{{.Config.Image}}')

ts=$(date +%Y%m%d-%H%M%S)
backup_dir="$COMPOSE_DIR/backups/release-$ts"
backup_tag="new-api-backup:$ts"
mkdir -p "$backup_dir"
cp -a "$COMPOSE_DIR/docker-compose.yml" "$COMPOSE_DIR/.env" "$backup_dir/"
docker inspect "$container_id" > "$backup_dir/container-inspect.json"
printf '%s\n' "$old_image_id" > "$backup_dir/old-image-id.txt"
python3 -c 'import sqlite3,sys; s=sqlite3.connect(sys.argv[1]); d=sqlite3.connect(sys.argv[2]); s.backup(d); d.close(); s.close()' "$DB_FILE" "$backup_dir/one-api.db"
docker image tag "$old_image_id" "$backup_tag"

gzip -dc "$work_dir/mad-new-api.tar.gz" | docker load
python3 -c 'import pathlib,re,sys; p=pathlib.Path(sys.argv[1]); s=p.read_text(); n=re.subn(r"(?m)^(\s+image:\s*).+$", r"\1"+sys.argv[2], s, count=1); assert n[1] == 1; p.write_text(n[0])' "$COMPOSE_DIR/docker-compose.yml" "$IMAGE"

cd "$COMPOSE_DIR"
docker compose up -d --force-recreate --no-deps "$SERVICE"

healthy=0
for _ in $(seq 1 60); do
  if curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null | grep -q '"success":true'; then
    healthy=1
    break
  fi
  sleep 2
done

if [ "$healthy" -eq 1 ] && activate_image_gateway; then
  printf '%s\n' "$release_sha" > "$STATE_FILE"
  install_updater
  install_cpa_deployer
  install_error_alert
  logger -t new-api-autoupdate "release deployed successfully: $release_sha; CPA containers left untouched"
  exit 0
fi

logger -t new-api-autoupdate "release health check failed; rolling back"
rollback_image_gateway
docker compose stop "$SERVICE" || true
cp -a "$backup_dir/one-api.db" "$DB_FILE"
cp -a "$backup_dir/docker-compose.yml" "$COMPOSE_DIR/docker-compose.yml"
docker image tag "$backup_tag" "$old_image_name"
docker compose up -d --force-recreate --no-deps "$SERVICE"

for _ in $(seq 1 45); do
  if curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null | grep -q '"success":true'; then
    logger -t new-api-autoupdate "rollback succeeded"
    exit 1
  fi
  sleep 2
done

logger -t new-api-autoupdate "rollback failed; manual intervention required"
exit 2
