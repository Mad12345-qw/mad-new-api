#!/bin/sh
set -eu

codex_home=${CODEX_HOME:-$HOME/.codex}

config_path=$codex_home/config.toml
[ -f "$config_path" ] || { printf '%s\n' 'MadAPI local history recovery skipped: config.toml is missing.' >&2; exit 1; }
provider=${MADAPI_HISTORY_PROVIDER:-}
if [ -z "$provider" ]; then
  provider=$(awk '
  /^[[:space:]]*\[/ { exit }
  /^[[:space:]]*model_provider[[:space:]]*=/ {
    line = $0
    sub(/^[^=]*=[[:space:]]*"/, "", line)
    sub(/".*/, "", line)
    print line
    exit
  }
' "$config_path")
fi
case "$provider" in
  ''|*[!A-Za-z0-9_-]*) printf '%s\n' 'MadAPI local history recovery skipped: current provider is invalid.' >&2; exit 1 ;;
esac

if ! command -v python3 >/dev/null 2>&1; then
  printf '%s\n' 'MadAPI local history recovery skipped: python3 is unavailable.' >&2
  exit 0
fi

CODEX_HOME="$codex_home" MADAPI_HISTORY_PROVIDER="$provider" MADAPI_HISTORY_BACKUP_DIR="${MADAPI_HISTORY_BACKUP_DIR:-}" python3 - <<'PY'
import json
import os
import shutil
import sqlite3
from datetime import datetime, timezone
from pathlib import Path

root = Path(os.environ["CODEX_HOME"])
provider = os.environ["MADAPI_HISTORY_PROVIDER"]
sessions = root / "sessions"
index = root / "session_index.jsonl"
state_path = root / ".codex-global-state.json"
database = root / "state_5.sqlite"
stamp = datetime.now().strftime("%Y%m%d-%H%M%S-%f")
backup_override = os.environ.get("MADAPI_HISTORY_BACKUP_DIR", "").strip()
backup = Path(backup_override) if backup_override else root / f"madapi-history-backup-{stamp}"
backup.mkdir(exist_ok=False)
if index.exists():
    shutil.copy2(index, backup / "session_index.jsonl.before")
if state_path.exists():
    shutil.copy2(state_path, backup / ".codex-global-state.json.before")
for suffix in ("", "-wal", "-shm"):
    source = Path(str(database) + suffix)
    if source.exists():
        shutil.copy2(source, backup / source.name)

migrated = 0
if database.exists() and provider != "openai":
    connection = sqlite3.connect(database)
    try:
        cursor = connection.execute(
            "UPDATE threads SET model_provider = ? WHERE archived = 0 AND source = 'vscode' AND thread_source = 'user' AND model_provider = 'openai'",
            (provider,),
        )
        migrated = cursor.rowcount
        connection.commit()
    finally:
        connection.close()

records = []
if sessions.is_dir():
    for path in sessions.rglob("rollout-*.jsonl"):
        try:
            first = path.open(encoding="utf-8").readline()
            event = json.loads(first)
            payload = event.get("payload", {})
            if event.get("type") != "session_meta" or not payload.get("id"):
                continue
            records.append({"id": payload["id"], "cwd": payload.get("cwd", "")})
        except (OSError, ValueError, json.JSONDecodeError):
            pass

assigned = 0
assigned_ids = set()
if state_path.exists():
    try:
        data = json.loads(state_path.read_text(encoding="utf-8"))
        def walk(value):
            if isinstance(value, dict):
                if "local-projects" in value:
                    yield value
                for child in value.values():
                    yield from walk(child)
            elif isinstance(value, list):
                for child in value:
                    yield from walk(child)
        record_ids = {item["id"] for item in records}
        for container in walk(data):
            hints = container.setdefault("thread-workspace-root-hints", {})
            mappings = container.setdefault("thread-project-assignments", {})
            for project_id, project in container["local-projects"].items():
                for root_path in project.get("rootPaths", []):
                    root_path = Path(root_path)
                    if not root_path.is_dir():
                        continue
                    for record in records:
                        cwd = Path(record["cwd"])
                        try:
                            cwd.relative_to(root_path)
                        except ValueError:
                            continue
                        hints[record["id"]] = str(root_path)
                        mappings[record["id"]] = {"projectKind": "local", "projectId": project_id, "cwd": record["cwd"], "pendingCoreUpdate": False}
                        assigned += 1
                        assigned_ids.add(record["id"])
            if "projectless-thread-ids" in container:
                container["projectless-thread-ids"] = [item for item in container["projectless-thread-ids"] if item not in assigned_ids]
        temp = state_path.with_suffix(".json.tmp")
        temp.write_text(json.dumps(data, ensure_ascii=False, separators=(",", ":")), encoding="utf-8")
        os.replace(temp, state_path)
    except (OSError, ValueError, json.JSONDecodeError) as error:
        print("MadAPI local project recovery skipped: " + str(error))

print(f"MadAPI local history recovered: {migrated} conversations, {assigned} project mappings.")
print(f"MadAPI local history provider migrated: {migrated} conversations to {provider}.")
print("History backup created: " + str(backup))
PY
