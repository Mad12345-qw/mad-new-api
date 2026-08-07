#!/usr/bin/env python3
"""Reconcile one native CPA gateway without touching unrelated services."""

from __future__ import annotations

import re
import sys
from pathlib import Path


SERVICE_RE = re.compile(r"^  [A-Za-z0-9_.-]+:\s*$")


def service_bounds(lines: list[str], name: str) -> tuple[int, int] | None:
    marker = f"  {name}:"
    for start, line in enumerate(lines):
        if line.rstrip("\r\n") != marker:
            continue
        end = start + 1
        while end < len(lines) and not SERVICE_RE.match(lines[end].rstrip("\r\n")):
            end += 1
        return start, end
    return None


def replace_service(lines: list[str], name: str, block: list[str]) -> list[str]:
    bounds = service_bounds(lines, name)
    if bounds is None:
        if block:
            if lines and lines[-1].strip():
                lines.append("\n")
            lines.extend(block)
        return lines
    start, end = bounds
    lines[start:end] = block
    return lines


def reconcile_compose(source: str) -> str:
    lines = source.splitlines(keepends=True)
    if not lines:
        raise ValueError("empty Compose file")
    if service_bounds(lines, "new-api") is None:
        raise ValueError("missing Compose service: new-api")

    native_block = [
        "  cpa-codex-native:\n",
        "    image: mad-cpa-codex:latest\n",
        "    container_name: cpa-codex-native\n",
        "    restart: unless-stopped\n",
        "    mem_limit: 512m\n",
        "    cpus: 0.75\n",
        "    ports:\n",
        '      - "127.0.0.1:8320:8317"\n',
        "    environment:\n",
        "      TZ: Asia/Shanghai\n",
        "      MADAPI_INTERNAL_URL: http://new-api:3000\n",
        "\n",
    ]
    lines = replace_service(lines, "cpa-codex-native", native_block)
    # The deployer starts this service before removing legacy containers, so
    # removing them from Compose cannot interrupt traffic before Nginx flips.
    lines = replace_service(lines, "cpa-codex", [])
    lines = replace_service(lines, "cpa-codex-cockpit", [])
    return "".join(lines)


def main() -> int:
    if len(sys.argv) != 2:
        raise SystemExit("usage: compose_reconcile.py /path/to/docker-compose.yml")
    path = Path(sys.argv[1])
    source = path.read_text(encoding="utf-8")
    reconciled = reconcile_compose(source)
    if reconciled != source:
        path.write_text(reconciled, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
