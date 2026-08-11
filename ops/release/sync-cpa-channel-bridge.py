#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import os
import secrets
import sqlite3
import tempfile
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit


SUPPORTED_NATIVE_TYPES = {14: "claude-api-key", 24: "gemini-api-key", 48: "xai-api-key"}
BRIDGE_TAG_PREFIX = "madapi-official-cpa-source:"


def quoted(value: object) -> str:
    return json.dumps(str(value), ensure_ascii=False)


def load_models(path: Path) -> list[str]:
    models = []
    for raw in path.read_text(encoding="utf-8").splitlines():
        model = raw.split("#", 1)[0].strip()
        if model and model not in models:
            models.append(model)
    if not models:
        raise ValueError("model file is empty")
    return models


def parse_mapping(raw: str | None) -> dict[str, str]:
    if not raw:
        return {}
    value = json.loads(raw)
    if not isinstance(value, dict):
        raise ValueError("model_mapping must be an object")
    return {str(key): str(item) for key, item in value.items()}


def parse_headers(raw: str | None) -> dict[str, str]:
    if not raw:
        return {}
    try:
        value = json.loads(raw)
    except json.JSONDecodeError:
        return {}
    if not isinstance(value, dict):
        return {}
    return {str(key): str(item) for key, item in value.items() if isinstance(item, (str, int, float, bool))}


def normalize_openai_url(value: str) -> str:
    value = value.rstrip("/")
    parts = urlsplit(value)
    path = parts.path.rstrip("/")
    if not path.endswith("/v1"):
        path += "/v1"
    return urlunsplit((parts.scheme, parts.netloc, path, parts.query, parts.fragment))


def atomic_write(path: Path, content: str, mode: int = 0o600) -> None:
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, mode)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def gateway_key(path: Path) -> str:
    if path.exists():
        value = path.read_text(encoding="ascii").strip()
        if len(value) < 32:
            raise ValueError("gateway API key is too short")
        return value
    value = "sk-mad-cpa-" + secrets.token_urlsafe(48)
    atomic_write(path, value + "\n")
    return value


def source_channels(connection: sqlite3.Connection, targets: set[str]) -> list[dict]:
    connection.row_factory = sqlite3.Row
    rows = connection.execute(
        "SELECT * FROM channels WHERE status = 1 AND (tag IS NULL OR tag NOT LIKE ?)",
        (BRIDGE_TAG_PREFIX + "%",),
    ).fetchall()
    result = []
    for row in rows:
        item = dict(row)
        models = [value.strip() for value in (item.get("models") or "").split(",") if value.strip()]
        matched = [model for model in models if model in targets]
        if not matched:
            continue
        item["matched_models"] = matched
        item["mapping"] = parse_mapping(item.get("model_mapping"))
        item["prefix"] = f"madch{item['id']}"
        result.append(item)
    return result


def clone_values(source: dict, columns: list[str], gateway_url: str, key: str) -> dict:
    clone = {column: source.get(column) for column in columns if column != "id"}
    public_models = source["matched_models"]
    mapping = {
        model: f"{source['prefix']}/{source['mapping'].get(model, model)}"
        for model in public_models
    }
    clone.update(
        {
            "type": 1,
            "key": key,
            "open_ai_organization": "",
            "test_model": public_models[0],
            "status": 1,
            "name": f"CPA/{source['id']}/{source.get('name') or 'channel'}",
            "test_time": 0,
            "response_time": 0,
            "base_url": gateway_url.rstrip("/"),
            "balance": 0,
            "balance_updated_time": 0,
            "models": ",".join(public_models),
            "group": "codex",
            "used_quota": 0,
            "model_mapping": json.dumps(mapping, ensure_ascii=False, separators=(",", ":")),
            "tag": BRIDGE_TAG_PREFIX + str(source["id"]),
            "header_override": "",
            "param_override": "",
            "remark": f"Official CPA bridge for source channel {source['id']}",
        }
    )
    return clone


def upsert_clones(connection: sqlite3.Connection, sources: list[dict], gateway_url: str, key: str) -> list[int]:
    columns = [row[1] for row in connection.execute("PRAGMA table_info(channels)")]
    clone_ids = []
    active_tags = set()
    for source in sources:
        values = clone_values(source, columns, gateway_url, key)
        tag = values["tag"]
        active_tags.add(tag)
        existing = connection.execute("SELECT id FROM channels WHERE tag = ?", (tag,)).fetchone()
        if existing:
            clone_id = int(existing[0])
            names = list(values)
            connection.execute(
                "UPDATE channels SET " + ",".join(f'\"{name}\" = ?' for name in names) + " WHERE id = ?",
                [values[name] for name in names] + [clone_id],
            )
        else:
            names = list(values)
            cursor = connection.execute(
                "INSERT INTO channels (" + ",".join(f'\"{name}\"' for name in names) + ") VALUES (" + ",".join("?" for _ in names) + ")",
                [values[name] for name in names],
            )
            clone_id = int(cursor.lastrowid)
        clone_ids.append(clone_id)
        connection.execute("DELETE FROM abilities WHERE channel_id = ?", (clone_id,))
        priority = values.get("priority") or 0
        weight = max(0, int(values.get("weight") or 0))
        for model in source["matched_models"]:
            connection.execute(
                'INSERT INTO abilities ("group",model,channel_id,enabled,priority,weight,tag) VALUES (?,?,?,?,?,?,?)',
                ("codex", model, clone_id, 1, priority, weight, values["tag"]),
            )
    stale = connection.execute(
        "SELECT id,tag FROM channels WHERE tag LIKE ?", (BRIDGE_TAG_PREFIX + "%",)
    ).fetchall()
    for clone_id, tag in stale:
        if tag not in active_tags:
            connection.execute("UPDATE channels SET status = 2 WHERE id = ?", (clone_id,))
            connection.execute("UPDATE abilities SET enabled = 0 WHERE channel_id = ?", (clone_id,))
    return clone_ids


def model_entries(source: dict) -> list[tuple[str, str, bool]]:
    entries = []
    seen = set()
    for public in source["matched_models"]:
        actual = source["mapping"].get(public, public)
        key = (actual, actual)
        if key in seen:
            continue
        seen.add(key)
        entries.append((actual, actual, "image" in public.lower()))
    return entries


def emit_headers(lines: list[str], headers: dict[str, str], indent: str) -> None:
    if not headers:
        return
    lines.append(indent + "headers:")
    for name in sorted(headers):
        lines.append(indent + f"  {quoted(name)}: {quoted(headers[name])}")


def build_config(sources: list[dict], ingress_key: str) -> str:
    lines = [
        'host: ""',
        "port: 8317",
        'auth-dir: "/data/auths"',
        "api-keys:",
        f"  - {quoted(ingress_key)}",
        "debug: false",
        "request-log: true",
        'disable-image-generation: "passthrough"',
        "force-model-prefix: true",
    ]
    grouped: dict[str, list[dict]] = {"openai-compatibility": []}
    for source in sources:
        section = SUPPORTED_NATIVE_TYPES.get(int(source["type"]), "openai-compatibility")
        grouped.setdefault(section, []).append(source)

    openai = grouped.get("openai-compatibility", [])
    if openai:
        lines.append("openai-compatibility:")
        for source in openai:
            lines.extend(
                [
                    f"  - name: {quoted('madapi-channel-' + str(source['id']))}",
                    f"    prefix: {quoted(source['prefix'])}",
                    f"    base-url: {quoted(normalize_openai_url(source['base_url']))}",
                ]
            )
            emit_headers(lines, parse_headers(source.get("header_override")), "    ")
            lines.extend(["    api-key-entries:", f"      - api-key: {quoted(source['key'])}", "    models:"])
            for actual, alias, image in model_entries(source):
                lines.extend([f"      - name: {quoted(actual)}", f"        alias: {quoted(alias)}"])
                if image:
                    lines.append("        image: true")

    for section in ("claude-api-key", "gemini-api-key", "xai-api-key"):
        entries = grouped.get(section, [])
        if not entries:
            continue
        lines.append(section + ":")
        for source in entries:
            base_url = normalize_openai_url(source["base_url"]) if section == "xai-api-key" else source["base_url"].rstrip("/")
            lines.extend(
                [
                    f"  - api-key: {quoted(source['key'])}",
                    f"    prefix: {quoted(source['prefix'])}",
                    f"    base-url: {quoted(base_url)}",
                ]
            )
            emit_headers(lines, parse_headers(source.get("header_override")), "    ")
            lines.append("    models:")
            for actual, alias, _image in model_entries(source):
                lines.extend([f"      - name: {quoted(actual)}", f"        alias: {quoted(alias)}"])
    return "\n".join(lines) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--database", required=True, type=Path)
    parser.add_argument("--models", required=True, type=Path)
    parser.add_argument("--output-config", required=True, type=Path)
    parser.add_argument("--gateway-key-file", required=True, type=Path)
    parser.add_argument("--gateway-base-url", required=True)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    targets = set(load_models(args.models))
    key = gateway_key(args.gateway_key_file)
    connection = sqlite3.connect(args.database)
    try:
        sources = source_channels(connection, targets)
        if not sources:
            raise RuntimeError("no active source channels match the configured Codex models")
        config = build_config(sources, key)
        if args.dry_run:
            clone_ids = []
        else:
            connection.execute("BEGIN IMMEDIATE")
            clone_ids = upsert_clones(connection, sources, args.gateway_base_url, key)
            connection.commit()
            atomic_write(args.output_config, config)
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()

    print(json.dumps({
        "source_channel_ids": [item["id"] for item in sources],
        "bridge_channel_ids": clone_ids,
        "models": sorted({model for item in sources for model in item["matched_models"]}),
        "provider_count": len(sources),
        "dry_run": args.dry_run,
    }, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
