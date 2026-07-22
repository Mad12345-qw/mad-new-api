#!/usr/bin/env python3
import base64
import binascii
import json
import logging
import os
import secrets
import signal
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


UPSTREAM = os.getenv("UPSTREAM", "http://127.0.0.1:3001")
PUBLIC_BASE_URL = os.getenv("PUBLIC_BASE_URL", "https://mad.myddns.me")
CACHE_DIR = Path(os.getenv("CACHE_DIR", "/opt/image-url-cache"))
CACHE_TTL = int(os.getenv("CACHE_TTL", "1800"))
CLEAN_INTERVAL = int(os.getenv("CLEAN_INTERVAL", "300"))
MAX_BODY = int(os.getenv("MAX_BODY", str(2 * 1024 * 1024)))
TIMEOUT = int(os.getenv("UPSTREAM_TIMEOUT", "650"))

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
LOG = logging.getLogger("image-url-compat")

HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailers",
    "transfer-encoding",
    "upgrade",
    "content-length",
    "content-encoding",
}
IMAGE2_MODELS = {"gpt-image-2", "gpt-image-2-4k"}


def is_image2_model(model):
    return isinstance(model, str) and model.strip().lower() in IMAGE2_MODELS


def image_extension(raw):
    if raw.startswith(b"\x89PNG\r\n\x1a\n"):
        return "png", "image/png"
    if raw.startswith(b"\xff\xd8\xff"):
        return "jpg", "image/jpeg"
    if raw.startswith(b"RIFF") and raw[8:12] == b"WEBP":
        return "webp", "image/webp"
    if len(raw) >= 12 and raw[4:8] == b"ftyp" and raw[8:12] in {
        b"avif",
        b"avis",
        b"mif1",
        b"msf1",
    }:
        return "avif", "image/avif"
    return None


def cleanup_cache():
    CACHE_DIR.mkdir(parents=True, exist_ok=True)
    cutoff = time.time() - CACHE_TTL
    removed = 0
    for path in CACHE_DIR.iterdir():
        try:
            if path.is_file() and path.stat().st_mtime < cutoff:
                path.unlink()
                removed += 1
        except FileNotFoundError:
            pass
        except OSError:
            LOG.exception("cache cleanup failed for %s", path)
    if removed:
        LOG.info("removed %d expired cache files", removed)


def cleanup_loop(stop_event):
    while not stop_event.wait(CLEAN_INTERVAL):
        cleanup_cache()


def prepare_upstream_body(body):
    try:
        request_json = json.loads(body)
    except (json.JSONDecodeError, UnicodeDecodeError):
        return {}, body

    if not isinstance(request_json, dict) or not is_image2_model(
        request_json.get("model")
    ):
        return request_json, body

    upstream_json = dict(request_json)
    upstream_json["response_format"] = "b64_json"
    upstream_body = json.dumps(
        upstream_json, ensure_ascii=False, separators=(",", ":")
    ).encode("utf-8")
    return request_json, upstream_body


def add_urls(payload, remove_base64=False):
    data = payload.get("data")
    if not isinstance(data, list):
        return 0

    CACHE_DIR.mkdir(parents=True, exist_ok=True)
    added = 0
    for item in data:
        if not isinstance(item, dict) or not isinstance(item.get("b64_json"), str):
            continue
        try:
            raw = base64.b64decode(item["b64_json"], validate=True)
        except (binascii.Error, ValueError):
            LOG.warning("skipped invalid base64 image")
            continue
        detected = image_extension(raw)
        if not detected:
            LOG.warning("skipped image with unsupported magic bytes")
            continue
        ext, _ = detected
        name = secrets.token_urlsafe(32) + "." + ext
        final_path = CACHE_DIR / name
        temp_path = CACHE_DIR / ("." + name + ".tmp")
        try:
            with open(temp_path, "xb") as handle:
                handle.write(raw)
                handle.flush()
                os.fsync(handle.fileno())
            os.chmod(temp_path, 0o640)
            os.replace(temp_path, final_path)
        finally:
            try:
                temp_path.unlink()
            except FileNotFoundError:
                pass
        item["url"] = PUBLIC_BASE_URL.rstrip("/") + "/image-cache/" + name
        if remove_base64:
            item.pop("b64_json", None)
        added += 1
    return added


def transform_image_response(request_json, content_type, response_body):
    if not (
        isinstance(request_json, dict)
        and is_image2_model(request_json.get("model"))
        and "application/json" in content_type.lower()
    ):
        return response_body, "passthrough"

    requested_format = str(request_json.get("response_format") or "").lower()
    if requested_format == "b64_json":
        return response_body, "b64_json"

    try:
        payload = json.loads(response_body)
    except (json.JSONDecodeError, UnicodeDecodeError):
        LOG.warning("successful upstream response was not valid JSON")
        return response_body, "passthrough"

    try:
        if requested_format == "url":
            mode = "url" if add_urls(payload, remove_base64=True) else "passthrough"
        else:
            mode = "dual" if add_urls(payload) else "b64_json"
        encoded = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode(
            "utf-8"
        )
        return encoded, mode
    except Exception:
        LOG.exception("failed to normalize image response")
        return response_body, "passthrough"


class Handler(BaseHTTPRequestHandler):
    server_version = "ImageURLCompat/2.0"
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        LOG.info("%s %s", self.address_string(), fmt % args)

    def send_json_error(self, status, message):
        raw = json.dumps(
            {"error": {"message": message, "type": "compat_error"}}
        ).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(raw)

    def upstream_headers(self, body_length=None):
        headers = {}
        for key, value in self.headers.items():
            if key.lower() not in HOP_HEADERS and key.lower() != "host":
                headers[key] = value
        if body_length is not None:
            headers["Content-Length"] = str(body_length)
        return headers

    def forward_response(self, status, response_headers, response_body, mode):
        self.send_response(status)
        for key, value in response_headers.items():
            if key.lower() not in HOP_HEADERS:
                self.send_header(key, value)
        self.send_header("Content-Length", str(len(response_body)))
        self.send_header("X-Image-URL-Compat", mode)
        self.end_headers()
        try:
            self.wfile.write(response_body)
        except (BrokenPipeError, ConnectionResetError):
            LOG.info("client disconnected before response completed")

    def request_upstream(self, method, body=None, headers=None):
        request = urllib.request.Request(
            UPSTREAM + self.path,
            data=body,
            headers=headers or {},
            method=method,
        )
        try:
            response = urllib.request.urlopen(request, timeout=TIMEOUT)
            return response.status, response.headers, response.read()
        except urllib.error.HTTPError as exc:
            return exc.code, exc.headers, exc.read()

    def do_GET(self):
        if self.path == "/health":
            raw = b'{"status":"ok"}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)
            return
        self.send_json_error(404, "not found")

    def do_OPTIONS(self):
        try:
            status, response_headers, response_body = self.request_upstream(
                "OPTIONS", headers=self.upstream_headers()
            )
            self.forward_response(status, response_headers, response_body, "passthrough")
        except Exception:
            LOG.exception("upstream OPTIONS request failed")
            self.send_json_error(502, "upstream request failed")

    def do_POST(self):
        if self.path.split("?", 1)[0] != "/v1/images/generations":
            self.send_json_error(404, "not found")
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            self.send_json_error(400, "invalid content length")
            return
        if length <= 0 or length > MAX_BODY:
            self.send_json_error(413, "request body is empty or too large")
            return

        body = self.rfile.read(length)
        request_json, upstream_body = prepare_upstream_body(body)
        try:
            status, response_headers, response_body = self.request_upstream(
                "POST",
                body=upstream_body,
                headers=self.upstream_headers(len(upstream_body)),
            )
        except Exception:
            LOG.exception("upstream request failed")
            self.send_json_error(502, "upstream request failed")
            return

        mode = "passthrough"
        content_type = response_headers.get("Content-Type", "")
        if 200 <= status < 300:
            response_body, mode = transform_image_response(
                request_json, content_type, response_body
            )
        self.forward_response(status, response_headers, response_body, mode)


def main():
    cleanup_cache()
    stop_event = threading.Event()
    cleaner = threading.Thread(target=cleanup_loop, args=(stop_event,), daemon=True)
    cleaner.start()
    server = ThreadingHTTPServer(("127.0.0.1", 3010), Handler)
    server.daemon_threads = True

    def stop(_signum, _frame):
        stop_event.set()
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    LOG.info("listening on 127.0.0.1:3010; upstream=%s ttl=%ss", UPSTREAM, CACHE_TTL)
    try:
        server.serve_forever(poll_interval=0.5)
    finally:
        stop_event.set()
        server.server_close()


if __name__ == "__main__":
    main()
