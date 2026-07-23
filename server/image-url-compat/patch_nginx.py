#!/usr/bin/env python3
import argparse
import os
import shutil
import subprocess
import tempfile
import time
from pathlib import Path


DEFAULT_SITE = Path("/etc/nginx/sites-enabled/mad.myddns.me")
DEFAULT_BACKUP_DIR = Path("/opt/new-api/backups/nginx")
INSERT_BEFORE = "    # image-url-compat managed block\n"
ROUTES = (
    ("/pg/images/generations", "playground-generation"),
    ("/v1/images/edits", "external-edits"),
    ("/pg/images/edits", "playground-edits"),
)


def route_block(path, label):
    return f"""    # image-url-compat {label} block
    location = {path} {{
        client_max_body_size 64m;
        proxy_pass http://127.0.0.1:3010;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Connection "";
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 700s;
        proxy_send_timeout 700s;
    }}

"""


PLAYGROUND_BLOCK = route_block(*ROUTES[0])


def patched_config(content):
    if INSERT_BEFORE not in content:
        raise RuntimeError("image compatibility marker not found")
    missing = [
        route_block(path, label)
        for path, label in ROUTES
        if f"location = {path}" not in content
    ]
    if not missing:
        return content, False
    return content.replace(INSERT_BEFORE, "".join(missing) + INSERT_BEFORE, 1), True


def write_atomic(path, content):
    mode = path.stat().st_mode
    with tempfile.NamedTemporaryFile(
        mode="w", encoding="utf-8", dir=path.parent, delete=False
    ) as handle:
        handle.write(content)
        temp_path = Path(handle.name)
    os.chmod(temp_path, mode)
    os.replace(temp_path, path)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--site", type=Path, default=DEFAULT_SITE)
    parser.add_argument("--backup-dir", type=Path, default=DEFAULT_BACKUP_DIR)
    parser.add_argument("--no-reload", action="store_true")
    args = parser.parse_args()

    original = args.site.read_text(encoding="utf-8")
    updated, changed = patched_config(original)
    if not changed:
        print("nginx playground route already configured")
        return

    args.backup_dir.mkdir(parents=True, exist_ok=True)
    backup = args.backup_dir / (
        args.site.name + ".pre-playground-image-compat-" + time.strftime("%Y%m%d-%H%M%S")
    )
    shutil.copy2(args.site, backup)
    write_atomic(args.site, updated)
    if args.no_reload:
        print("nginx playground route configured without reload")
        return

    try:
        subprocess.run(["nginx", "-t"], check=True)
        subprocess.run(["systemctl", "reload", "nginx"], check=True)
    except Exception:
        shutil.copy2(backup, args.site)
        subprocess.run(["nginx", "-t"], check=False)
        raise
    print("nginx playground route configured and reloaded")


if __name__ == "__main__":
    main()
