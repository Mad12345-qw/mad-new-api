#!/usr/bin/env python3
"""Replace only the managed MadAPI locations in a complete nginx site file."""

from __future__ import annotations

import sys
from pathlib import Path


MANAGED = (
    "location ^~ /codex/v1/ {",
    "location ^~ /v1/ {",
    "location = /v1/images/generations {",
    "location ^~ /mad-codex/ {",
)

PROXY_ONLY = (
    "location ^~ /codex/cockpit/v1/ {",
    "location / {",
)


def block_end(lines: list[str], start: int) -> int:
    depth = 0
    opened = False
    for index in range(start, len(lines)):
        depth += lines[index].count("{")
        if "{" in lines[index]:
            opened = True
        depth -= lines[index].count("}")
        if opened and depth == 0:
            return index + 1
    raise ValueError(f"unterminated nginx block at line {start + 1}")


def find_block(lines: list[str], prefix: str) -> int | None:
    matches = [index for index, line in enumerate(lines) if line.strip().startswith(prefix)]
    if len(matches) > 1:
        raise ValueError(f"duplicate managed nginx location: {prefix}")
    return matches[0] if matches else None


def patch(site: str, snippet: str) -> str:
    newline = "\r\n" if "\r\n" in site else "\n"
    site_lines = site.replace("\r\n", "\n").splitlines()
    snippet_lines = snippet.replace("\r\n", "\n").splitlines()

    managed_blocks: dict[str, list[str]] = {}
    for prefix in MANAGED:
        start = find_block(snippet_lines, prefix)
        if start is None:
            raise ValueError(f"snippet is missing {prefix}")
        managed_blocks[prefix] = snippet_lines[start:block_end(snippet_lines, start)]

    replacements: list[tuple[int, int, list[str]]] = []
    for prefix in MANAGED:
        start = find_block(site_lines, prefix)
        if start is not None:
            replacements.append((start, block_end(site_lines, start), managed_blocks[prefix]))

    for start, end, replacement in sorted(replacements, reverse=True):
        while start > 0 and not site_lines[start - 1].strip():
            start -= 1
        site_lines[start:end] = replacement

    new_api_proxy = next(
        (
            line.strip()
            for line in managed_blocks["location ^~ /v1/ {"]
            if line.strip().startswith("proxy_pass ")
        ),
        None,
    )
    if new_api_proxy is None:
        raise ValueError("unified snippet has no NewAPI proxy_pass")
    for prefix in PROXY_ONLY:
        start = find_block(site_lines, prefix)
        if start is None:
            raise ValueError(f"site is missing required NewAPI location: {prefix}")
        end = block_end(site_lines, start)
        proxy_lines = [
            index
            for index in range(start, end)
            if site_lines[index].strip().startswith("proxy_pass ")
        ]
        if len(proxy_lines) != 1:
            raise ValueError(f"expected one proxy_pass in {prefix}")
        indentation = site_lines[proxy_lines[0]][: len(site_lines[proxy_lines[0]]) - len(site_lines[proxy_lines[0]].lstrip())]
        site_lines[proxy_lines[0]] = indentation + new_api_proxy

    missing = [prefix for prefix in MANAGED if find_block(site_lines, prefix) is None]
    if missing:
        marker = find_block(site_lines, "location ^~ /codex/v1/ {")
        if marker is None:
            raise ValueError("site has no Codex location insertion point")
        insertion = []
        for prefix in missing:
            if insertion:
                insertion.append("")
            insertion.extend(managed_blocks[prefix])
        site_lines[marker:marker] = insertion + [""]

    return newline.join(site_lines) + newline


def main() -> int:
    if len(sys.argv) != 4:
        print("usage: patch-nginx-unified-route.py <site> <snippet> <output>", file=sys.stderr)
        return 64
    site, snippet, output = map(Path, sys.argv[1:])
    rendered = patch(site.read_text(encoding="utf-8"), snippet.read_text(encoding="utf-8"))
    output.write_text(rendered, encoding="utf-8", newline="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
