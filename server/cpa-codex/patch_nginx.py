#!/usr/bin/env python3
"""Route public Codex traffic through MadAPI's authenticated controllers."""

from __future__ import annotations

import shutil
import subprocess
import sys
from datetime import datetime
from pathlib import Path


CONFIG_PATH = Path("/etc/nginx/sites-enabled/mad.myddns.me")
BACKUP_DIR = Path("/opt/new-api/backups/nginx")
BEGIN_MARKER = "# MadAPI CPA Codex front routes begin"
END_MARKER = "# MadAPI CPA Codex front routes end"
LEGACY_MARKER = "# MadAPI CPA Codex sidecar"
LEGACY_PROXY = "proxy_pass http://127.0.0.1:8318/v1/;"

MODEL_PROXY = """        proxy_pass http://127.0.0.1:3001;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Authorization $http_authorization;
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 60s;
        proxy_send_timeout 60s;"""

CODEX_PROXY = """        proxy_pass http://127.0.0.1:3001;
        client_max_body_size 64m;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 600s;
        proxy_send_timeout 600s;"""

MANAGED_BLOCK = f"""    {BEGIN_MARKER}
    location = /codex/v1/models {{
{MODEL_PROXY}
    }}

    location = /codex/cockpit/v1/models {{
{MODEL_PROXY}
    }}

    location ^~ /codex/v1/ {{
{CODEX_PROXY}
    }}

    location ^~ /codex/cockpit/v1/ {{
{CODEX_PROXY}
    }}
    {END_MARKER}

"""


def remove_marked_range(source: str) -> str:
    lines = source.splitlines(keepends=True)
    begin = next((index for index, line in enumerate(lines) if BEGIN_MARKER in line), None)
    if begin is None:
        return source
    end = next((index for index in range(begin + 1, len(lines)) if END_MARKER in lines[index]), None)
    if end is None:
        raise RuntimeError("incomplete managed CPA Codex route block")
    end += 1
    while end < len(lines) and not lines[end].strip():
        end += 1
    del lines[begin:end]
    return "".join(lines)


def remove_legacy_route(source: str) -> str:
    lines = source.splitlines(keepends=True)
    for marker_index, line in enumerate(lines):
        if LEGACY_MARKER not in line:
            continue
        location_index = marker_index + 1
        while location_index < len(lines) and not lines[location_index].strip():
            location_index += 1
        if location_index >= len(lines) or "location ^~ /codex/v1/" not in lines[location_index]:
            continue
        depth = 0
        end = location_index
        opened = False
        while end < len(lines):
            depth += lines[end].count("{")
            depth -= lines[end].count("}")
            opened = opened or "{" in lines[end]
            end += 1
            if opened and depth == 0:
                break
        block = "".join(lines[marker_index:end])
        if not opened or depth != 0 or LEGACY_PROXY not in block:
            continue
        while end < len(lines) and not lines[end].strip():
            end += 1
        del lines[marker_index:end]
        return "".join(lines)
    return source


def reconcile_routes(source: str) -> str:
    cleaned = remove_legacy_route(remove_marked_range(source))
    if "location ^~ /codex/v1/" in cleaned or "location ^~ /codex/cockpit/v1/" in cleaned:
        raise RuntimeError("an unmanaged Codex execution route already exists")
    anchor = "    location / {"
    if cleaned.count(anchor) != 1:
        raise RuntimeError("unable to find a unique MadAPI default Nginx location")
    return cleaned.replace(anchor, MANAGED_BLOCK + anchor, 1)


def main() -> int:
    if not CONFIG_PATH.is_file():
        raise RuntimeError(f"missing Nginx configuration: {CONFIG_PATH}")
    source = CONFIG_PATH.read_text(encoding="utf-8")
    reconciled = reconcile_routes(source)
    if reconciled == source:
        return 0

    BACKUP_DIR.mkdir(parents=True, exist_ok=True)
    backup = BACKUP_DIR / f"mad.myddns.me.before-cpa-front-{datetime.utcnow():%Y%m%d-%H%M%S}"
    shutil.copy2(CONFIG_PATH, backup)
    CONFIG_PATH.write_text(reconciled, encoding="utf-8")
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
