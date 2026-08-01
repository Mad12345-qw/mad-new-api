#!/usr/bin/env python3
"""Install the CPA-only Codex route after the sidecar is healthy."""

from __future__ import annotations

import shutil
import subprocess
import sys
from datetime import datetime
from pathlib import Path


# This host keeps sites-enabled as an independent active file rather than a
# symlink, so update the configuration Nginx actually loads.
CONFIG_PATH = Path("/etc/nginx/sites-enabled/mad.myddns.me")
BACKUP_DIR = Path("/opt/new-api/backups/nginx")
MARKER = "# MadAPI CPA Codex sidecar"
LOCATION = """    # MadAPI CPA Codex sidecar
    location ^~ /codex/v1/ {
        proxy_pass http://127.0.0.1:8318/v1/;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-MadAPI-Authorization $http_authorization;
        proxy_set_header Authorization "Bearer madapi-codex-gateway";
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 600s;
        proxy_send_timeout 600s;
    }

"""


def main() -> int:
    if not CONFIG_PATH.is_file():
        raise RuntimeError(f"missing Nginx configuration: {CONFIG_PATH}")
    source = CONFIG_PATH.read_text(encoding="utf-8")
    if MARKER in source:
        return 0
    anchor = "    location / {"
    if source.count(anchor) != 1:
        raise RuntimeError("unable to find a unique MadAPI default Nginx location")

    BACKUP_DIR.mkdir(parents=True, exist_ok=True)
    for legacy_backup in CONFIG_PATH.parent.glob("mad.myddns.me.before-cpa-codex-*"):
        shutil.move(str(legacy_backup), BACKUP_DIR / legacy_backup.name)
    backup = BACKUP_DIR / f"mad.myddns.me.before-cpa-codex-{datetime.utcnow():%Y%m%d-%H%M%S}"
    shutil.copy2(CONFIG_PATH, backup)
    CONFIG_PATH.write_text(source.replace(anchor, LOCATION + anchor), encoding="utf-8")
    check = subprocess.run(["nginx", "-t"], text=True, capture_output=True, check=False)
    if check.returncode == 0:
        reload_result = subprocess.run(["systemctl", "reload", "nginx"], text=True, capture_output=True, check=False)
        if reload_result.returncode == 0:
            return 0
    shutil.copy2(backup, CONFIG_PATH)
    subprocess.run(["nginx", "-t"], text=True, capture_output=True, check=False)
    subprocess.run(["systemctl", "reload", "nginx"], text=True, capture_output=True, check=False)
    return 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print(f"CPA Codex Nginx route update failed: {error}", file=sys.stderr)
        raise SystemExit(1)
