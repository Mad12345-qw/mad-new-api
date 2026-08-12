#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 1 ]]; then echo "usage: $0 <unified-backup-directory>" >&2; exit 64; fi
backup_dir="$1"
site="${MADAPI_NGINX_SITE:-/etc/nginx/sites-enabled/mad.myddns.me}"
network="${MADAPI_DOCKER_NETWORK:-new-api_default}"
health_attempts="${MADAPI_UNIFIED_HEALTH_ATTEMPTS:-120}"
[[ -d "$backup_dir" && -f "$backup_dir/nginx.site.before.conf" && -f "$backup_dir/release.env" ]]
for command in docker curl nginx sed systemctl; do command -v "$command" >/dev/null; done
get_value() { sed -n "s/^$1=//p" "$backup_dir/release.env" | tail -n 1; }
old_new="$(get_value MADAPI_UNIFIED_OLD_NEW_API)"
old_cpa="$(get_value MADAPI_UNIFIED_OLD_CPA)"
old_control="$(get_value MADAPI_UNIFIED_OLD_CONTROL)"
old_cpa_present="$(get_value MADAPI_UNIFIED_OLD_CPA_PRESENT)"
old_control_present="$(get_value MADAPI_UNIFIED_OLD_CONTROL_PRESENT)"
old_port="$(get_value MADAPI_UNIFIED_OLD_PORT)"
candidate_new="$(get_value MADAPI_UNIFIED_CANDIDATE_NEW_API)"
candidate_cpa="$(get_value MADAPI_UNIFIED_CANDIDATE_CPA)"
legacy_watchdog_timer="$(get_value MADAPI_UNIFIED_LEGACY_WATCHDOG_TIMER)"
watchdog_enabled="$(get_value MADAPI_UNIFIED_LEGACY_WATCHDOG_ENABLED)"
watchdog_active="$(get_value MADAPI_UNIFIED_LEGACY_WATCHDOG_ACTIVE)"
legacy_autoupdate_timer="$(get_value MADAPI_UNIFIED_LEGACY_AUTOUPDATE_TIMER)"
autoupdate_enabled="$(get_value MADAPI_UNIFIED_LEGACY_AUTOUPDATE_ENABLED)"
autoupdate_active="$(get_value MADAPI_UNIFIED_LEGACY_AUTOUPDATE_ACTIVE)"
for value in "$old_new" "$candidate_new" "$candidate_cpa"; do [[ "$value" =~ ^[A-Za-z0-9._-]+$ ]]; done
docker inspect "$old_new" >/dev/null
[[ "$old_cpa_present" == 0 || "$old_cpa_present" == 1 ]]
[[ "$old_control_present" == 0 || "$old_control_present" == 1 ]]
[[ "$old_cpa_present" == 0 ]] || docker inspect "$old_cpa" >/dev/null
[[ "$old_control_present" == 0 ]] || docker inspect "$old_control" >/dev/null

# Stop the current writers before the old pair reopens the same SQLite database.
docker stop "$candidate_new" "$candidate_cpa" >/dev/null
docker rename "$old_new" new-api
[[ "$old_cpa_present" == 0 ]] || docker rename "$old_cpa" cpa-official-gateway
[[ "$old_control_present" == 0 ]] || docker rename "$old_control" new-api-codex-control
docker start new-api >/dev/null
[[ "$old_cpa_present" == 0 ]] || docker start cpa-official-gateway >/dev/null
[[ "$old_control_present" == 0 ]] || docker start new-api-codex-control >/dev/null
restore_timer_state() {
  timer="$1"; enabled="$2"; active="$3"
  [[ -z "$timer" || "$enabled" != enabled ]] || systemctl enable "$timer" >/dev/null 2>&1
  [[ -z "$timer" || "$active" != active ]] || systemctl start "$timer" >/dev/null 2>&1
}

new_status=""; cpa_status="not-present"
for _ in $(seq 1 "$health_attempts"); do
  new_status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:$old_port/api/status" || true)"
  if [[ "$old_cpa_present" == 1 ]]; then
    cpa_status="$(docker run --rm --network "$network" curlimages/curl:8.12.1 -sS -o /dev/null -w '%{http_code}' --max-time 3 http://cpa-official-gateway:18417/healthz || true)"
  fi
  [[ "$new_status" == 200 && ( "$old_cpa_present" == 0 || "$cpa_status" == 200 ) ]] && break
  sleep 0.5
done
if [[ "$new_status" != 200 || ( "$old_cpa_present" == 1 && "$cpa_status" != 200 ) ]]; then
  docker stop new-api cpa-official-gateway new-api-codex-control >/dev/null 2>&1 || true
  docker rename new-api "$old_new" >/dev/null 2>&1 || true
  [[ "$old_cpa_present" == 0 ]] || docker rename cpa-official-gateway "$old_cpa" >/dev/null 2>&1 || true
  [[ "$old_control_present" == 0 ]] || docker rename new-api-codex-control "$old_control" >/dev/null 2>&1 || true
  docker start "$candidate_cpa" "$candidate_new" >/dev/null 2>&1 || true
  echo "rollback target health failed; candidate restarted and Nginx was not changed" >&2
  exit 69
fi

restore_timer_state "$legacy_watchdog_timer" "$watchdog_enabled" "$watchdog_active"
restore_timer_state "$legacy_autoupdate_timer" "$autoupdate_enabled" "$autoupdate_active"

cp -a "$backup_dir/nginx.site.before.conf" "$site"
nginx -t
nginx -s reload
public_url="${MADAPI_PUBLIC_URL:-}"
if [[ -n "$public_url" ]]; then
  edge_status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 15 "${public_url%/}/codex/v1" || true)"
  [[ "$edge_status" =~ ^[234][0-9][0-9]$ ]]
fi
printf 'unified_rollback=ok backup=%s newapi=%s cpa=%s sqlite_single_writer=true\n' "$backup_dir" "$new_status" "$cpa_status"
