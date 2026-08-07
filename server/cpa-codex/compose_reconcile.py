#!/usr/bin/env python3
"""Reconcile the native and Cockpit CPA front proxies."""

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


def replace_service(lines: list[str], name: str, block: list[str]) -> list[str]:
    bounds = service_bounds(lines, name)
    if bounds is None:
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
    lines = ensure_environment(
        lines,
        "new-api",
        {
            "MADAPI_CODEX_DISPATCH_TOKEN": "${MADAPI_CODEX_DISPATCH_TOKEN}",
            "MADAPI_CPA_DISPATCH_URL": "http://cpa-codex:8317/internal/madapi/codex/execute",
            "MADAPI_CPA_IMAGE_DISPATCH_URL": "http://cpa-codex:8317/internal/madapi/codex/image",
            "MADAPI_CPA_CANARY_DISPATCH_URL": "http://cpa-codex-canary:8317/internal/madapi/codex/execute",
            "MADAPI_CPA_CANARY_COCKPIT_DISPATCH_URL": "http://cpa-codex-cockpit-canary:8317/internal/madapi/codex/execute",
            "MADAPI_INTERNAL_CATALOG_TOKEN": "${MADAPI_INTERNAL_CATALOG_TOKEN}",
        },
    )

    native_block = [
        "  cpa-codex:\n",
        "    image: mad-cpa-codex:latest\n",
        "    container_name: cpa-codex\n",
        "    restart: unless-stopped\n",
        "    mem_limit: 512m\n",
        "    cpus: 0.75\n",
        "    ports:\n",
        '      - "127.0.0.1:8318:8317"\n',
        "    extra_hosts:\n",
        '      - "host.docker.internal:host-gateway"\n',
        "    volumes:\n",
        "      - ./cpa-codex:/data\n",
        "    environment:\n",
        "      TZ: Asia/Shanghai\n",
        "      MADAPI_CODEX_DISPATCH_TOKEN: ${MADAPI_CODEX_DISPATCH_TOKEN}\n",
        "      MADAPI_INTERNAL_URL: http://new-api:3000\n",
        "      MADAPI_IMAGE_COMPAT_URL: http://host.docker.internal:3010\n",
        "      MADAPI_INTERNAL_CATALOG_TOKEN: ${MADAPI_INTERNAL_CATALOG_TOKEN}\n",
        "      CPA_CATALOG_MODE: native\n",
        "      CPA_CONFIG_PATH: /data/native-config.yaml\n",
        "\n",
    ]
    lines = replace_service(lines, "cpa-codex", native_block)

    cockpit_block = [
        "  cpa-codex-cockpit:\n",
        "    image: mad-cpa-codex:latest\n",
        "    container_name: cpa-codex-cockpit\n",
        "    restart: unless-stopped\n",
        "    mem_limit: 384m\n",
        "    cpus: 0.50\n",
        "    ports:\n",
        '      - "127.0.0.1:8319:8317"\n',
        "    extra_hosts:\n",
        '      - "host.docker.internal:host-gateway"\n',
        "    volumes:\n",
        "      - ./cpa-codex:/data\n",
        "    environment:\n",
        "      TZ: Asia/Shanghai\n",
        "      MADAPI_INTERNAL_URL: http://new-api:3000\n",
        "      MADAPI_IMAGE_COMPAT_URL: http://host.docker.internal:3010\n",
        "      MADAPI_INTERNAL_CATALOG_TOKEN: ${MADAPI_INTERNAL_CATALOG_TOKEN}\n",
        "      CPA_CATALOG_MODE: cockpit\n",
        "      CPA_CONFIG_PATH: /data/cockpit-config.yaml\n",
        "\n",
    ]
    lines = replace_service(lines, "cpa-codex-cockpit", cockpit_block)
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
