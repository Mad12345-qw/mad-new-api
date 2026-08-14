#!/usr/bin/env python3
from __future__ import annotations

import hashlib
import json
import sys
import urllib.error
import urllib.request
from pathlib import Path


def fetch(base: str, path: str) -> tuple[int, bytes]:
    url = base.rstrip("/") + ("/" if path == "/" else path)
    request = urllib.request.Request(url, headers={"User-Agent": "MadAPI-release-gate/1.0"})
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            return response.status, response.read(64 * 1024 * 1024)
    except urllib.error.HTTPError as error:
        return error.code, error.read()


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: verify-frozen-ui.py <base-url> <metadata.json>", file=sys.stderr)
        return 64
    base, metadata_path = sys.argv[1:]
    metadata = json.loads(Path(metadata_path).read_text(encoding="utf-8"))
    checked = 0
    for item in metadata["files"]:
        status, body = fetch(base, item["path"])
        actual = hashlib.sha256(body).hexdigest()
        if status != 200 or actual != item["sha256"]:
            raise SystemExit(
                f"frozen UI mismatch: path={item['path']} status={status} "
                f"expected={item['sha256']} actual={actual}"
            )
        checked += 1
    print(json.dumps({"frozen_ui_files": checked, "all_hashes_match": True}, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
