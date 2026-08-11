#!/usr/bin/env python3
"""Render the production Compose file with the isolated CPA SDK host."""

from __future__ import annotations

import argparse
import re
from pathlib import Path


MANAGED_ENV = {
    "MADAPI_CPA_SDK_DISPATCH_TOKEN": "${MADAPI_CPA_SDK_DISPATCH_TOKEN}",
    "MADAPI_CPA_SDK_DISPATCH_URL": "http://cpa-sdk-host:18417/execute",
    "TRUSTED_ROUTE_GROUP": "codex",
    "TRUSTED_ROUTE_PRESERVE_USER_GROUP": '"true"',
    "TRUSTED_ROUTE_TOKEN": "${TRUSTED_ROUTE_TOKEN}",
}


def indentation(line: str) -> int:
    return len(line) - len(line.lstrip(" "))


def block_end(lines: list[str], start: int, indent: int, limit: int) -> int:
    for index in range(start + 1, limit):
        stripped = lines[index].strip()
        if stripped and not stripped.startswith("#") and indentation(lines[index]) <= indent:
            return index
    return limit


def find_mapping(lines: list[str], name: str, indent: int, start: int, end: int) -> int:
    expected = " " * indent + name + ":"
    matches = [index for index in range(start, end) if lines[index].rstrip() == expected]
    if len(matches) != 1:
        raise ValueError(f"expected exactly one {name!r} mapping, found {len(matches)}")
    return matches[0]


def render(source: str) -> str:
    newline = "\r\n" if "\r\n" in source else "\n"
    lines = source.replace("\r\n", "\n").splitlines()
    services = find_mapping(lines, "services", 0, 0, len(lines))
    services_end = block_end(lines, services, 0, len(lines))
    new_api = find_mapping(lines, "new-api", 2, services + 1, services_end)
    new_api_end = block_end(lines, new_api, 2, services_end)
    environment = find_mapping(lines, "environment", 4, new_api + 1, new_api_end)
    environment_end = block_end(lines, environment, 4, new_api_end)

    key_pattern = re.compile(r"^\s{6}([A-Za-z_][A-Za-z0-9_]*)\s*:")
    retained: list[str] = []
    for line in lines[environment + 1 : environment_end]:
        match = key_pattern.match(line)
        if match and match.group(1) in MANAGED_ENV:
            continue
        retained.append(line)
    managed = [f"      {key}: {value}" for key, value in MANAGED_ENV.items()]
    lines[environment + 1 : environment_end] = retained + managed

    services_end = block_end(lines, services, 0, len(lines))
    existing = [
        index
        for index in range(services + 1, services_end)
        if lines[index].rstrip() == "  cpa-sdk-host:"
    ]
    if len(existing) > 1:
        raise ValueError("multiple cpa-sdk-host services are not supported")
    if existing:
        old_start = existing[0]
        old_end = block_end(lines, old_start, 2, services_end)
        if old_start > services + 1 and not lines[old_start - 1].strip():
            old_start -= 1
        del lines[old_start:old_end]
        services_end = block_end(lines, services, 0, len(lines))

    service = ([] if services_end > 0 and not lines[services_end - 1].strip() else [""]) + [
        "  cpa-sdk-host:",
        "    image: mad-cpa-sdk-host:latest",
        "    container_name: cpa-sdk-host",
        "    restart: unless-stopped",
        "    mem_limit: 256m",
        "    memswap_limit: 384m",
        "    cpus: 1.0",
        "    pids_limit: 256",
        "    environment:",
        "      TZ: Asia/Shanghai",
        "      MADAPI_CPA_SDK_DISPATCH_TOKEN: ${MADAPI_CPA_SDK_DISPATCH_TOKEN}",
        "    healthcheck:",
        '      test: ["CMD-SHELL", "wget -q -O - http://127.0.0.1:18417/healthz >/dev/null || exit 1"]',
        "      interval: 10s",
        "      timeout: 5s",
        "      retries: 6",
        "      start_period: 20s",
    ]
    lines[services_end:services_end] = service
    return newline.join(lines) + newline


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("input", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    rendered = render(args.input.read_text(encoding="utf-8"))
    args.output.write_text(rendered, encoding="utf-8", newline="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
