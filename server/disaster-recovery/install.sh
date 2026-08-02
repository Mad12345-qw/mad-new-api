#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "Run this installer as root." >&2
  exit 2
fi

SOURCE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
INSTALL_DIR=/usr/local/lib/madapi-disaster-recovery
KEY_DIR=/etc/madapi-backup
REPO_DIR=/opt/madapi-private-config-backups

test -f "$SOURCE_DIR/backup.py"
test -f "$SOURCE_DIR/restore.py"
test -f "$KEY_DIR/recovery-public.pem"
test -d "$REPO_DIR/.git"

install -d -m 0755 "$INSTALL_DIR"
install -m 0755 "$SOURCE_DIR/backup.py" "$INSTALL_DIR/backup.py"
install -m 0755 "$SOURCE_DIR/restore.py" "$INSTALL_DIR/restore.py"

for unit in \
  madapi-local-backup.service \
  madapi-local-backup.timer \
  madapi-offsite-backup.service \
  madapi-offsite-backup.timer; do
  install -m 0644 "$SOURCE_DIR/$unit" "/etc/systemd/system/$unit"
done

systemctl daemon-reload
systemctl enable --now madapi-local-backup.timer madapi-offsite-backup.timer
systemctl start madapi-local-backup.service

