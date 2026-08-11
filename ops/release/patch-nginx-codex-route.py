#!/usr/bin/env python3
"""Replace the managed Codex route block without touching other Nginx routes."""

from __future__ import annotations

import argparse
from pathlib import Path


BEGIN = "    # MadAPI native CPA Codex routes begin"
END = "    # MadAPI native CPA Codex routes end"


def render(site: str, snippet: str) -> str:
    if site.count(BEGIN) != 1 or site.count(END) != 1:
        raise ValueError("the Nginx site must contain exactly one managed Codex route block")
    start = site.index(BEGIN)
    finish = site.index(END, start) + len(END)
    indented = "\n".join(("    " + line) if line else "" for line in snippet.strip().splitlines())
    replacement = BEGIN + "\n" + indented + "\n" + END
    return site[:start] + replacement + site[finish:]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("site", type=Path)
    parser.add_argument("snippet", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    result = render(
        args.site.read_text(encoding="utf-8"),
        args.snippet.read_text(encoding="utf-8"),
    )
    args.output.write_text(result, encoding="utf-8", newline="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
