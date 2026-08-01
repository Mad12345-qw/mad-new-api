#!/usr/bin/env python3
"""Refresh the internal CPA model registry from MadAPI without persisting keys."""

from __future__ import annotations

import json
import os
import sys
import tempfile
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


CONFIG_PATH = Path(os.environ.get("CPA_CONFIG_PATH", "/data/config.yaml"))
MADAPI_INTERNAL_URL = os.environ.get("MADAPI_INTERNAL_URL", "http://new-api:3000").rstrip("/")
CATALOG_TOKEN = os.environ.get("MADAPI_INTERNAL_CATALOG_TOKEN", "").strip()


def allowed_model_ids(payload: dict[str, Any]) -> list[str]:
    data = payload.get("data")
    if not isinstance(data, list):
        raise ValueError("MadAPI catalog does not contain a data array")

    model_ids: list[str] = []
    seen: set[str] = set()
    for item in data:
        if not isinstance(item, dict):
            continue
        model_id = item.get("id")
        if not isinstance(model_id, str):
            continue
        model_id = model_id.strip()
        if not model_id or model_id in seen:
            continue

        endpoint_types = item.get("supported_endpoint_types")
        if isinstance(endpoint_types, list) and endpoint_types:
            normalized = {str(value).strip().lower() for value in endpoint_types}
            if not normalized.intersection({"openai", "openai-response", "anthropic", "gemini"}):
                continue

        seen.add(model_id)
        model_ids.append(model_id)

    if not model_ids:
        raise ValueError("MadAPI catalog has no supported text models")
    return model_ids


def render_config(model_ids: list[str]) -> str:
    lines = [
        'host: "0.0.0.0"',
        "port: 8317",
        "logging-to-file: false",
        "request-retry: 0",
        "max-retry-credentials: 1",
        "disable-cooling: true",
        "api-keys:",
        '  - "madapi-codex-gateway"',
        "openai-compatibility:",
        '  - name: "madapi"',
        f"    base-url: {json.dumps(MADAPI_INTERNAL_URL + '/v1')}",
        "    madapi-passthrough: true",
        "    disable-cooling: true",
        "    api-key-entries:",
        '      - api-key: "madapi-codex-selector"',
        "    models:",
    ]
    for model_id in model_ids:
        encoded = json.dumps(model_id, ensure_ascii=True)
        lines.append(f"      - name: {encoded}")
        lines.append(f"        alias: {encoded}")
        lines.append(f"        display-name: {encoded}")
        lines.append("        input-modalities: [text]")
    return "\n".join(lines) + "\n"


def fetch_catalog() -> dict[str, Any]:
    if not CATALOG_TOKEN:
        raise RuntimeError("MADAPI_INTERNAL_CATALOG_TOKEN is required")
    request = urllib.request.Request(
        f"{MADAPI_INTERNAL_URL}/internal/codex/models",
        headers={"X-MadAPI-Catalog-Token": CATALOG_TOKEN, "Accept": "application/json"},
    )
    with urllib.request.urlopen(request, timeout=20) as response:
        if response.status != 200:
            raise RuntimeError(f"MadAPI catalog returned HTTP {response.status}")
        return json.load(response)


def write_config(content: str) -> None:
    CONFIG_PATH.parent.mkdir(parents=True, exist_ok=True)
    if CONFIG_PATH.exists():
        # CPA watches this path directly. Preserve its inode on refresh so the
        # watcher continues receiving updates after the first synchronization.
        with CONFIG_PATH.open("r+", encoding="utf-8") as handle:
            handle.seek(0)
            handle.write(content)
            handle.truncate()
            handle.flush()
            os.fsync(handle.fileno())
        return
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=CONFIG_PATH.parent, delete=False) as handle:
        handle.write(content)
        handle.flush()
        os.fsync(handle.fileno())
        staged_path = Path(handle.name)
    os.replace(staged_path, CONFIG_PATH)


def main() -> int:
    try:
        models = allowed_model_ids(fetch_catalog())
        write_config(render_config(models))
        print(f"MadAPI CPA catalog synchronized: {len(models)} models", flush=True)
        return 0
    except (OSError, ValueError, RuntimeError, urllib.error.URLError, json.JSONDecodeError) as error:
        print(f"MadAPI CPA catalog synchronization failed: {error}", file=sys.stderr, flush=True)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
