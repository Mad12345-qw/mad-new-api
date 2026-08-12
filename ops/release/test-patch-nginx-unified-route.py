#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import tempfile


path = Path(__file__).with_name("patch-nginx-unified-route.py")
spec = importlib.util.spec_from_file_location("patcher", path)
assert spec and spec.loader
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

site = """server {
    listen 443 ssl;
    location /health {
        return 200;
    }
    location ^~ /v1/ {
        proxy_pass http://old-v1;
    }
    location ^~ /codex/v1/ {
        proxy_pass http://old-codex;
    }
    location = /v1/images/generations {
        proxy_pass http://old-image;
    }
    location ^~ /mad-codex/ {
        proxy_pass http://old-assets;
    }
}
"""
snippet = Path(__file__).with_name("nginx-unified-route.conf.template").read_text(encoding="utf-8")
with tempfile.TemporaryDirectory() as directory:
    output = Path(directory) / "site.conf"
    output.write_text(module.patch(site, snippet), encoding="utf-8")
    result = output.read_text(encoding="utf-8")
assert "proxy_pass http://127.0.0.1:__NEW_API_PORT__;" in result
assert "proxy_pass http://127.0.0.1:__IMAGE_PORT__;" in result
assert "location /health" in result
assert "proxy_pass http://old-v1;" not in result
assert "proxy_pass http://old-codex;" not in result
assert "proxy_pass http://old-image;" not in result
assert "proxy_pass http://old-assets;" not in result
assert "location /health" in result
print("nginx_unified_patcher_test=passed")
