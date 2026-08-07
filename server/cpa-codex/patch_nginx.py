#!/usr/bin/env python3
"""Install stable or canary Codex routes with an atomic Nginx reload."""

from __future__ import annotations

import argparse
import shutil
import subprocess
import sys
from datetime import datetime
from pathlib import Path


CONFIG_PATH = Path("/etc/nginx/sites-enabled/mad.myddns.me")
BACKUP_DIR = Path("/opt/new-api/backups/nginx")
RETRY_GUARD_PATH = Path("/etc/nginx/conf.d/madapi-codex-retry-guard.conf")
BEGIN_MARKER = "# MadAPI CPA Codex front routes begin"
END_MARKER = "# MadAPI CPA Codex front routes end"
LEGACY_MARKER = "# MadAPI CPA Codex sidecar"
LEGACY_PROXY = "proxy_pass http://127.0.0.1:8318/v1/;"

RETRY_GUARD_CONFIG = """# Managed by MadAPI CPA Codex deployment.\n# Only the Codex Responses front route uses this zone; ordinary /v1 traffic is untouched.\nlimit_req_zone $binary_remote_addr zone=madapi_codex_responses_per_ip:10m rate=1r/s;\n"""

RETRY_GUARD = """        limit_req zone=madapi_codex_responses_per_ip burst=1 nodelay;
        limit_req_status 429;
        add_header Retry-After 3 always;
"""

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

DIRECT_PROXY = """        proxy_pass http://127.0.0.1:3001;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header X-MadAPI-Codex-Canary 1;
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 600s;
        proxy_send_timeout 600s;"""

DIRECT_BODY = DIRECT_PROXY.removeprefix("        proxy_pass http://127.0.0.1:3001;\n")
DIRECT_COCKPIT_PROXY = DIRECT_PROXY.replace(
    "proxy_set_header X-MadAPI-Codex-Canary 1;",
    "proxy_set_header X-MadAPI-Codex-Canary 1;\n        proxy_set_header X-MadAPI-Codex-Cockpit 1;",
)
DIRECT_COCKPIT_BODY = DIRECT_COCKPIT_PROXY.removeprefix("        proxy_pass http://127.0.0.1:3001;\n")

CPA_PROXY = """        client_max_body_size 64m;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-MadAPI-Authorization $http_authorization;
        proxy_set_header Authorization "Bearer madapi-codex-gateway";
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 600s;
        proxy_send_timeout 600s;"""


def managed_block(mode: str) -> str:
    if mode not in {"stable", "direct"}:
        raise ValueError(f"unsupported Codex route mode: {mode}")
    primary_proxy = CPA_PROXY if mode == "stable" else DIRECT_PROXY
    primary_cockpit_proxy = CPA_PROXY if mode == "stable" else DIRECT_COCKPIT_PROXY
    primary_native = "http://127.0.0.1:8318/v1/;" if mode == "stable" else None
    primary_cockpit = "http://127.0.0.1:8319/v1/;" if mode == "stable" else None
    native_body = f"        proxy_pass {primary_native}\n{primary_proxy}" if primary_native else primary_proxy
    cockpit_body = f"        proxy_pass {primary_cockpit}\n{primary_cockpit_proxy}" if primary_cockpit else primary_cockpit_proxy
    native_responses_body = (
        f"        proxy_pass http://127.0.0.1:8318/v1/responses;\n{CPA_PROXY}"
        if mode == "stable"
        else DIRECT_PROXY
    )
    cockpit_responses_body = (
        f"        proxy_pass http://127.0.0.1:8319/v1/responses;\n{CPA_PROXY}"
        if mode == "stable"
        else DIRECT_COCKPIT_PROXY
    )
    return f"""    {BEGIN_MARKER}
    # mode: {mode}
    location = /codex/v1/models {{
{MODEL_PROXY}
    }}

    location = /codex/cockpit/v1/models {{
{MODEL_PROXY}
    }}

    # Stops a failed Codex client from repeatedly entering CPA. It does not apply to /v1.
    location = /codex/v1/responses {{
{RETRY_GUARD}{native_responses_body}
    }}

    location = /codex/cockpit/v1/responses {{
{RETRY_GUARD}{cockpit_responses_body}
    }}

    location ^~ /codex/v1/ {{
{native_body}
    }}

    location ^~ /codex/cockpit/v1/ {{
{cockpit_body}
    }}

    location = /codex-canary/v1/models {{
        proxy_pass http://127.0.0.1:3001/codex/v1/models;
{DIRECT_BODY}
    }}

    location = /codex-canary/cockpit/v1/models {{
        proxy_pass http://127.0.0.1:3001/codex/cockpit/v1/models;
{DIRECT_COCKPIT_BODY}
    }}

    location ^~ /codex-canary/v1/ {{
        proxy_pass http://127.0.0.1:3001/codex/v1/;
{DIRECT_BODY}
    }}

    location ^~ /codex-canary/cockpit/v1/ {{
        proxy_pass http://127.0.0.1:3001/codex/cockpit/v1/;
{DIRECT_COCKPIT_BODY}
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


def reconcile_routes(source: str, mode: str = "stable") -> str:
    cleaned = remove_legacy_route(remove_marked_range(source))
    for location in ("location ^~ /codex/v1/", "location ^~ /codex/cockpit/v1/", "location ^~ /codex-canary/v1/"):
        if location in cleaned:
            raise RuntimeError("an unmanaged Codex execution route already exists")
    anchor = "    location / {"
    if cleaned.count(anchor) != 1:
        raise RuntimeError("unable to find a unique MadAPI default Nginx location")
    return cleaned.replace(anchor, managed_block(mode) + anchor, 1)


def write_retry_guard() -> tuple[bool, str | None]:
    previous = RETRY_GUARD_PATH.read_text(encoding="utf-8") if RETRY_GUARD_PATH.exists() else None
    if previous == RETRY_GUARD_CONFIG:
        return False, previous
    RETRY_GUARD_PATH.parent.mkdir(parents=True, exist_ok=True)
    RETRY_GUARD_PATH.write_text(RETRY_GUARD_CONFIG, encoding="utf-8")
    return True, previous


def restore_retry_guard(previous: str | None) -> None:
    if previous is None:
        RETRY_GUARD_PATH.unlink(missing_ok=True)
    else:
        RETRY_GUARD_PATH.write_text(previous, encoding="utf-8")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("stable", "direct"), default="stable")
    args = parser.parse_args(argv)
    if not CONFIG_PATH.is_file():
        raise RuntimeError(f"missing Nginx configuration: {CONFIG_PATH}")
    source = CONFIG_PATH.read_text(encoding="utf-8")
    reconciled = reconcile_routes(source, args.mode)
    BACKUP_DIR.mkdir(parents=True, exist_ok=True)
    backup = BACKUP_DIR / f"mad.myddns.me.before-cpa-front-{datetime.utcnow():%Y%m%d-%H%M%S}"
    if reconciled != source:
        shutil.copy2(CONFIG_PATH, backup)
        CONFIG_PATH.write_text(reconciled, encoding="utf-8")
    guard_changed, previous_guard = write_retry_guard()
    if reconciled == source and not guard_changed:
        return 0
    check = subprocess.run(["nginx", "-t"], text=True, capture_output=True, check=False)
    if check.returncode == 0:
        reload_result = subprocess.run(["systemctl", "reload", "nginx"], text=True, capture_output=True, check=False)
        if reload_result.returncode == 0:
            return 0
    if reconciled != source:
        shutil.copy2(backup, CONFIG_PATH)
    restore_retry_guard(previous_guard)
    subprocess.run(["nginx", "-t"], text=True, capture_output=True, check=False)
    subprocess.run(["systemctl", "reload", "nginx"], text=True, capture_output=True, check=False)
    return 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print(f"CPA Codex Nginx route update failed: {error}", file=sys.stderr)
        raise SystemExit(1)
