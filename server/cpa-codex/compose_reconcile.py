#!/usr/bin/env python3
"""Reconcile the private CPA executor without changing public routes."""

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


def ensure_environment(lines: list[str], service: str, values: dict[str, str]) -> list[str]:
    bounds = service_bounds(lines, service)
    if bounds is None:
        raise ValueError(f"missing Compose service: {service}")
    start, end = bounds
    environment = None
    for index in range(start + 1, end):
        if lines[index].rstrip("\r\n") == "    environment:":
            environment = index
            break
    if environment is None:
        lines.insert(end, "    environment:\n")
        environment = end
        end += 1

    env_end = environment + 1
    while env_end < end:
        stripped = lines[env_end].strip()
        indent = len(lines[env_end]) - len(lines[env_end].lstrip(" "))
        if stripped and indent <= 4:
            break
        env_end += 1

    for key, value in values.items():
        pattern = re.compile(rf"^      {re.escape(key)}:")
        replacement = f"      {key}: {value}\n"
        for index in range(environment + 1, env_end):
            if pattern.match(lines[index]):
                lines[index] = replacement
                break
        else:
            lines.insert(env_end, replacement)
            env_end += 1
            end += 1
    return lines


def reconcile_compose(source: str) -> str:
    lines = source.splitlines(keepends=True)
    if not lines:
        raise ValueError("empty Compose file")
    lines = ensure_environment(
        lines,
        "new-api",
        {
            "MADAPI_CODEX_DISPATCH_TOKEN": "${MADAPI_CODEX_DISPATCH_TOKEN}",
            "MADAPI_CPA_DISPATCH_URL": "http://cpa-codex:8317/internal/madapi/codex/execute",
        },
    )

    block = [
        "  cpa-codex:\n",
        "    image: mad-cpa-codex:latest\n",
        "    container_name: cpa-codex\n",
        "    restart: unless-stopped\n",
        "    ports:\n",
        '      - "127.0.0.1:8318:8317"\n',
        "    volumes:\n",
        "      - ./cpa-codex:/data\n",
        "    environment:\n",
        "      TZ: Asia/Shanghai\n",
        "      MADAPI_CODEX_DISPATCH_TOKEN: ${MADAPI_CODEX_DISPATCH_TOKEN}\n",
    ]
    bounds = service_bounds(lines, "cpa-codex")
    if bounds is None:
        if lines and lines[-1].strip():
            lines.append("\n")
        lines.extend(block)
    else:
        start, end = bounds
        lines[start:end] = block
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
