#!/usr/bin/env python3
from __future__ import annotations

import pathlib
import sys


MARKER = "# MadAPI frozen v3 UI split begin"
FALLBACK = "    location / {\n        proxy_pass http://127.0.0.1:3001;"
INSERT = """    # MadAPI frozen v3 UI split begin
    location = /api {
        proxy_pass http://127.0.0.1:3001;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location ^~ /api/ {
        proxy_pass http://127.0.0.1:3001;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_read_timeout 600s;
        proxy_send_timeout 600s;
    }

    location = /v1 {
        proxy_pass http://127.0.0.1:3001;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header Authorization $http_authorization;
    }

    location /v1/ {
        proxy_pass http://127.0.0.1:3001;
        client_max_body_size 100m;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 700s;
        proxy_send_timeout 700s;
    }

    location ^~ /mad-codex/ {
        proxy_pass http://127.0.0.1:3001;
        proxy_set_header Host $host;
    }

    location ^~ /mad-claude/ {
        proxy_pass http://127.0.0.1:3001;
        proxy_set_header Host $host;
    }
    # MadAPI frozen v3 UI split end

"""


def main() -> int:
    if len(sys.argv) != 3:
        raise SystemExit("usage: patch-nginx-v3-solid-ui.py <input> <output>")
    source = pathlib.Path(sys.argv[1])
    target = pathlib.Path(sys.argv[2])
    text = source.read_text(encoding="utf-8")
    if MARKER in text:
        raise SystemExit("frozen v3 UI split already present")
    if text.count(FALLBACK) != 1:
        raise SystemExit("expected exactly one NewAPI fallback location")
    text = text.replace(FALLBACK, INSERT + "    location / {\n        proxy_pass http://127.0.0.1:13004;", 1)
    target.write_text(text, encoding="utf-8", newline="\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
