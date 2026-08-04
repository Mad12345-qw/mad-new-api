#!/bin/sh
set -eu

codex_home=${CODEX_HOME:-$HOME/.codex}
if [ ! -d "$codex_home/sessions" ]; then
  printf '%s\n' 'MadAPI local history recovery: no existing sessions.'
  exit 0
fi

if ! command -v python3 >/dev/null 2>&1; then
  printf '%s\n' 'MadAPI local history recovery skipped: python3 is unavailable.' >&2
  exit 0
fi

CODEX_HOME="$codex_home" python3 - <<'PY'
import json
import os
import shutil
from datetime import datetime, timezone
from pathlib import Path

root = Path(os.environ["CODEX_HOME"])
sessions = root / "sessions"
index = root / "session_index.jsonl"
state_path = root / ".codex-global-state.json"
stamp = datetime.now().strftime("%Y%m%d-%H%M%S-%f")
backup = root / f"madapi-history-backup-{stamp}"
backup.mkdir(exist_ok=False)
if index.exists():
    shutil.copy2(index, backup / "session_index.jsonl.before")
if state_path.exists():
    shutil.copy2(state_path, backup / ".codex-global-state.json.before")

titles = {}
if index.exists():
    for line in index.read_text(encoding="utf-8").splitlines():
        try:
            item = json.loads(line)
            if item.get("id") and item.get("thread_name"):
                titles[item["id"]] = item["thread_name"]
        except json.JSONDecodeError:
            pass

records = []
for path in sessions.rglob("rollout-*.jsonl"):
    try:
        first = path.open(encoding="utf-8").readline()
        event = json.loads(first)
        payload = event.get("payload", {})
        if event.get("type") != "session_meta" or not payload.get("id"):
            continue
        raw_time = payload.get("timestamp", "")
        parsed = datetime.fromisoformat(raw_time.replace("Z", "+00:00")) if raw_time else datetime.fromtimestamp(0, timezone.utc)
        records.append({"id": payload["id"], "cwd": payload.get("cwd", ""), "updated_at": parsed.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")})
    except (OSError, ValueError, json.JSONDecodeError):
        pass
records.sort(key=lambda item: (item["updated_at"], item["id"]))

with index.with_suffix(".jsonl.tmp").open("w", encoding="utf-8", newline="\n") as handle:
    for record in records:
        title = titles.get(record["id"]) or Path(record["cwd"]).name or "Recovered conversation"
        handle.write(json.dumps({"id": record["id"], "thread_name": title, "updated_at": record["updated_at"]}, ensure_ascii=False) + "\n")
os.replace(index.with_suffix(".jsonl.tmp"), index)

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

print(f"MadAPI local history recovered: {len(records)} conversations, {assigned} project mappings.")
print("History backup created: " + str(backup))
PY
