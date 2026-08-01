#!/usr/bin/env python3
"""Restore /codex/v1 to NewAPI by removing the obsolete front sidecar route."""

from __future__ import annotations

import shutil
import subprocess
import sys
from datetime import datetime
from pathlib import Path


CONFIG_PATH = Path("/etc/nginx/sites-enabled/mad.myddns.me")
BACKUP_DIR = Path("/opt/new-api/backups/nginx")
MARKER = "# MadAPI CPA Codex sidecar"
LEGACY_PROXY = "proxy_pass http://127.0.0.1:8318/v1/;"


def remove_legacy_sidecar_routes(source: str) -> tuple[str, int]:
    """Remove only the managed 8318 Codex location, preserving all other config."""
    lines = source.splitlines(keepends=True)
    removed = 0
    index = 0

    while index < len(lines):
        if MARKER not in lines[index]:
            index += 1
            continue

        marker_index = index
        location_index = marker_index + 1
        while location_index < len(lines) and not lines[location_index].strip():
            location_index += 1
        if location_index >= len(lines) or "location ^~ /codex/v1/" not in lines[location_index]:
            index += 1
            continue

        depth = 0
        end_index = location_index
        found_opening = False
        while end_index < len(lines):
            depth += lines[end_index].count("{")
            depth -= lines[end_index].count("}")
            found_opening = found_opening or "{" in lines[end_index]
            end_index += 1
            if found_opening and depth == 0:
                break
        if not found_opening or depth != 0:
            raise RuntimeError("malformed managed Codex location block")

        block = "".join(lines[marker_index:end_index])
        if LEGACY_PROXY not in block:
            index += 1
            continue

        while end_index < len(lines) and not lines[end_index].strip():
            end_index += 1
        del lines[marker_index:end_index]
        removed += 1
        index = marker_index

    return "".join(lines), removed


def main() -> int:
    if not CONFIG_PATH.is_file():
        raise RuntimeError(f"missing Nginx configuration: {CONFIG_PATH}")

    BACKUP_DIR.mkdir(parents=True, exist_ok=True)
    for legacy_backup in CONFIG_PATH.parent.glob("mad.myddns.me.before-cpa-codex-*"):
        shutil.move(str(legacy_backup), BACKUP_DIR / legacy_backup.name)

    source = CONFIG_PATH.read_text(encoding="utf-8")
    restored, removed = remove_legacy_sidecar_routes(source)
    if removed == 0:
        return 0
    if "location / {" not in restored:
        raise RuntimeError("default NewAPI Nginx location is missing after route restore")

    backup = BACKUP_DIR / f"mad.myddns.me.before-codex-route-restore-{datetime.utcnow():%Y%m%d-%H%M%S}"
    shutil.copy2(CONFIG_PATH, backup)
    CONFIG_PATH.write_text(restored, encoding="utf-8")

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
        print(f"Codex Nginx route restore failed: {error}", file=sys.stderr)
        raise SystemExit(1)
