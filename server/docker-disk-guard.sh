#!/bin/sh
set -eu

LOCK_FILE=/run/lock/mad-docker-disk-guard.lock
ROOT_MOUNT=/
WARN_PERCENT=${MAD_DOCKER_DISK_WARN_PERCENT:-80}
JOURNAL_RETENTION=${MAD_DOCKER_JOURNAL_RETENTION:-14d}

disk_percent() {
  df -P "$ROOT_MOUNT" | awk 'NR == 2 { gsub(/%/, "", $5); print $5 }'
}

log_usage() {
  phase=$1
  used=$(disk_percent)
  logger -t mad-docker-disk-guard "$phase root_disk_used_percent=$used"
  printf '%s root_disk_used_percent=%s\n' "$phase" "$used"
}

exec 9>"$LOCK_FILE"
flock -n 9 || exit 0

before=$(disk_percent)
log_usage before

# These commands never remove running containers, attached volumes, or application data.
docker container prune -f
docker image prune -af
docker builder prune -af
docker network prune -f
journalctl --vacuum-time="$JOURNAL_RETENTION"

after=$(disk_percent)
log_usage after

if [ "$after" -ge "$WARN_PERCENT" ]; then
  logger -p user.warning -t mad-docker-disk-guard \
    "disk remains above threshold after safe cleanup: used_percent=$after threshold=$WARN_PERCENT"
  exit 1
fi

if [ "$after" -gt "$before" ]; then
  logger -p user.warning -t mad-docker-disk-guard \
    "disk usage increased during maintenance: before=$before after=$after"
fi
