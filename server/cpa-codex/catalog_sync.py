#!/usr/bin/env python3
"""Build CPA front-proxy configs from MadAPI's enabled text models."""

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
CATALOG_MODE = os.environ.get("CPA_CATALOG_MODE", "native").strip().lower()

COCKPIT_TARGETS = (
    ("claude-fable-5", "gpt-5.5"),
    ("claude-opus-5", "gpt-5.4"),
    ("gpt-5.6-sol", "gpt-5.6-sol"),
    ("gpt-5.6-terra", "gpt-5.6-terra"),
    ("gpt-5.6-luna", "gpt-5.6-luna"),
    ("grok-4.5", "gpt-5.4-mini"),
    ("kimi-k3", "gpt-5.3-codex"),
    ("deepseek-v4-flash", "gpt-5.2"),
)


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


def configured_models(model_ids: list[str], mode: str) -> list[tuple[str, str, bool]]:
    if mode == "native":
        return [(model_id, model_id, False) for model_id in model_ids]
    if mode != "cockpit":
        raise ValueError(f"unsupported CPA catalog mode: {mode}")
    available = set(model_ids)
    models = [(upstream, client, True) for upstream, client in COCKPIT_TARGETS if upstream in available]
    if len(models) != len(COCKPIT_TARGETS):
        missing = [upstream for upstream, _ in COCKPIT_TARGETS if upstream not in available]
        raise ValueError("MadAPI Cockpit catalog is missing: " + ", ".join(missing))
    return models


def render_config(model_ids: list[str], mode: str) -> str:
    models = configured_models(model_ids, mode)
    lines = [
        'host: "0.0.0.0"',
        "port: 8317",
        "logging-to-file: false",
        "request-log: false",
        "request-retry: 2",
        "max-retry-credentials: 3",
        "disable-cooling: true",
        "api-keys:",
        '  - "madapi-codex-gateway"',
        "openai-compatibility:",
        f'  - name: "madapi-{mode}"',
        f"    base-url: {json.dumps(MADAPI_INTERNAL_URL + '/codex/v1')}",
        "    madapi-passthrough: true",
        "    disable-cooling: true",
        "    api-key-entries:",
        f'      - api-key: "madapi-{mode}-selector-1"',
        f'      - api-key: "madapi-{mode}-selector-2"',
        f'      - api-key: "madapi-{mode}-selector-3"',
        "    models:",
    ]
    for upstream, client, force_mapping in models:
        lines.append(f"      - name: {json.dumps(upstream, ensure_ascii=True)}")
        lines.append(f"        alias: {json.dumps(client, ensure_ascii=True)}")
        lines.append(f"        display-name: {json.dumps(upstream, ensure_ascii=True)}")
        if force_mapping:
            lines.append("        force-mapping: true")
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
        write_config(render_config(models, CATALOG_MODE))
        print(f"MadAPI CPA {CATALOG_MODE} catalog synchronized: {len(configured_models(models, CATALOG_MODE))} models", flush=True)
        return 0
    except (OSError, ValueError, RuntimeError, urllib.error.URLError, json.JSONDecodeError) as error:
        print(f"MadAPI CPA catalog synchronization failed: {error}", file=sys.stderr, flush=True)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
