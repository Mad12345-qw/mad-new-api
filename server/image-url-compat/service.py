#!/usr/bin/env python3
import base64
import binascii
import json
import logging
import os
import secrets
import signal
import re
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
IMAGE_DOWNLOAD_TIMEOUT = int(os.getenv("IMAGE_DOWNLOAD_TIMEOUT", "180"))
MAX_IMAGE_BYTES = int(os.getenv("MAX_IMAGE_BYTES", str(64 * 1024 * 1024)))

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
GEMINI_IMAGE_MODELS = {
    "gemini-3.1-flash-image-preview",
    "gemini-3-pro-image-preview",
}
IMAGE_GENERATION_PATHS = {
    "/v1/images/generations",
    "/pg/images/generations",
}


def is_image2_model(model):
    return isinstance(model, str) and model.strip().lower() in IMAGE2_MODELS


def is_image2_4k_model(model):
    return isinstance(model, str) and model.strip().lower() == "gpt-image-2-4k"


def is_gemini_image_model(model):
    return isinstance(model, str) and model.strip().lower() in GEMINI_IMAGE_MODELS


def gemini_image_size(request_json):
    explicit = str(request_json.get("image_size") or "").strip().upper()
    if explicit in {"1K", "2K", "4K"}:
        return explicit
    size = str(request_json.get("size") or "").strip().upper()
    if size in {"1K", "2K", "4K"}:
        return size
    match = re.fullmatch(r"(\d{2,5})[Xx](\d{2,5})", size)
    if not match:
        return "1K"
    longest = max(int(match.group(1)), int(match.group(2)))
    if longest > 2048:
        return "4K"
    if longest > 1024:
        return "2K"
    return "1K"


def gemini_chat_path(path):
    route, separator, query = path.partition("?")
    target = "/pg/chat/completions" if route.startswith("/pg/") else "/v1/chat/completions"
    return target + (separator + query if separator else "")


def build_gemini_chat_body(request_json):
    image_config = {"image_size": gemini_image_size(request_json)}
    aspect_ratio = str(request_json.get("aspect_ratio") or "").strip()
    if aspect_ratio:
        image_config["aspect_ratio"] = aspect_ratio
    payload = {
        "model": request_json.get("model"),
        "messages": [
            {
                "role": "user",
                "content": str(request_json.get("prompt") or "Generate an image"),
            }
        ],
        "stream": False,
        "extra_body": {"google": {"image_config": image_config}},
    }
    group = request_json.get("group")
    if isinstance(group, str) and group.strip():
        payload["group"] = group
    return json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


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
    upstream_json["response_format"] = (
        "url" if is_image2_4k_model(request_json.get("model")) else "b64_json"
    )
    upstream_body = json.dumps(
        upstream_json, ensure_ascii=False, separators=(",", ":")
    ).encode("utf-8")
    return request_json, upstream_body


def prepare_upstream_request(path, body):
    request_json, upstream_body = prepare_upstream_body(body)
    if isinstance(request_json, dict) and is_gemini_image_model(
        request_json.get("model")
    ):
        return request_json, gemini_chat_path(path), build_gemini_chat_body(request_json)
    return request_json, path, upstream_body


def decode_base64_image(value):
    try:
        return base64.b64decode(value, validate=True)
    except (binascii.Error, ValueError) as exc:
        raise ValueError("invalid base64 image") from exc


def download_image(url):
    request = urllib.request.Request(
        url,
        headers={
            "User-Agent": (
                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
                "AppleWebKit/537.36 Chrome/150 Safari/537.36"
            ),
            "Accept": "image/avif,image/webp,image/apng,image/*,*/*;q=0.8",
        },
    )
    with urllib.request.urlopen(request, timeout=IMAGE_DOWNLOAD_TIMEOUT) as response:
        chunks = []
        total = 0
        while True:
            chunk = response.read(1024 * 1024)
            if not chunk:
                break
            total += len(chunk)
            if total > MAX_IMAGE_BYTES:
                raise ValueError("upstream image is too large")
            chunks.append(chunk)
    return b"".join(chunks)


def store_image(raw):
    detected = image_extension(raw)
    if not detected:
        raise ValueError("unsupported image format")
    ext, _ = detected
    CACHE_DIR.mkdir(parents=True, exist_ok=True)
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
    return PUBLIC_BASE_URL.rstrip("/") + "/image-cache/" + name


def normalize_images(payload, requested_format):
    data = payload.get("data")
    if not isinstance(data, list):
        return 0

    normalized = 0
    for item in data:
        if not isinstance(item, dict):
            continue
        if isinstance(item.get("b64_json"), str):
            raw = decode_base64_image(item["b64_json"])
        elif isinstance(item.get("url"), str):
            raw = download_image(item["url"])
        else:
            continue

        if not image_extension(raw):
            raise ValueError("unsupported image format")
        if requested_format == "b64_json":
            item["b64_json"] = base64.b64encode(raw).decode("ascii")
            item.pop("url", None)
        elif requested_format == "url":
            item["url"] = store_image(raw)
            item.pop("b64_json", None)
        else:
            item["b64_json"] = base64.b64encode(raw).decode("ascii")
            item["url"] = store_image(raw)
        normalized += 1
    return normalized


def find_data_images(value):
    found = []
    if isinstance(value, str):
        for match in re.finditer(
            r"data:(image/[^;\s]+);base64,([A-Za-z0-9+/=]+)", value
        ):
            found.append((match.group(1), match.group(2)))
        return found
    if isinstance(value, list):
        for item in value:
            found.extend(find_data_images(item))
        return found
    if not isinstance(value, dict):
        return found
    inline = value.get("inline_data") or value.get("inlineData")
    if isinstance(inline, dict) and isinstance(inline.get("data"), str):
        found.append(
            (
                inline.get("mime_type") or inline.get("mimeType") or "image/png",
                inline["data"],
            )
        )
    image_url = value.get("image_url") or value.get("imageUrl")
    if isinstance(image_url, dict):
        found.extend(find_data_images(image_url.get("url")))
    elif isinstance(image_url, str):
        found.extend(find_data_images(image_url))
    for key, item in value.items():
        if key not in {"inline_data", "inlineData", "image_url", "imageUrl"}:
            found.extend(find_data_images(item))
    return found


def transform_gemini_chat_response(request_json, response_body):
    try:
        payload = json.loads(response_body)
    except (json.JSONDecodeError, UnicodeDecodeError):
        return response_body, "passthrough"
    images = find_data_images(payload.get("choices", []))
    if not images:
        LOG.warning("Gemini chat response did not contain image data")
        return response_body, "passthrough"
    data = []
    for _mime_type, encoded in images:
        raw = decode_base64_image(encoded)
        if not image_extension(raw):
            raise ValueError("unsupported Gemini image format")
        data.append({"b64_json": base64.b64encode(raw).decode("ascii")})
    image_payload = {"created": int(time.time()), "data": data}
    requested_format = str(request_json.get("response_format") or "").lower()
    normalize_images(image_payload, requested_format)
    mode = requested_format if requested_format in {"url", "b64_json"} else "dual"
    return json.dumps(
        image_payload, ensure_ascii=False, separators=(",", ":")
    ).encode("utf-8"), "gemini-" + mode


def transform_image_response(request_json, content_type, response_body):
    if (
        isinstance(request_json, dict)
        and is_gemini_image_model(request_json.get("model"))
        and "application/json" in content_type.lower()
    ):
        return transform_gemini_chat_response(request_json, response_body)
    if not (
        isinstance(request_json, dict)
        and is_image2_model(request_json.get("model"))
        and "application/json" in content_type.lower()
    ):
        return response_body, "passthrough"

    requested_format = str(request_json.get("response_format") or "").lower()
    if requested_format == "b64_json" and not is_image2_4k_model(
        request_json.get("model")
    ):
        return response_body, "b64_json"

    try:
        payload = json.loads(response_body)
    except (json.JSONDecodeError, UnicodeDecodeError):
        LOG.warning("successful upstream response was not valid JSON")
        return response_body, "passthrough"

    normalized = normalize_images(payload, requested_format)
    if not normalized:
        return response_body, "passthrough"
    mode = requested_format if requested_format in {"url", "b64_json"} else "dual"
    encoded = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode(
        "utf-8"
    )
    return encoded, mode


class Handler(BaseHTTPRequestHandler):
    server_version = "ImageURLCompat/4.0"
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

    def request_upstream(self, method, body=None, headers=None, path=None):
        request = urllib.request.Request(
            UPSTREAM + (path or self.path),
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
        if self.path.split("?", 1)[0] not in IMAGE_GENERATION_PATHS:
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
        request_json, upstream_path, upstream_body = prepare_upstream_request(
            self.path, body
        )
        try:
            status, response_headers, response_body = self.request_upstream(
                "POST",
                body=upstream_body,
                headers=self.upstream_headers(len(upstream_body)),
                path=upstream_path,
            )
        except Exception:
            LOG.exception("upstream request failed")
            self.send_json_error(502, "upstream request failed")
            return

        mode = "passthrough"
        content_type = response_headers.get("Content-Type", "")
        if 200 <= status < 300:
            try:
                response_request_json = request_json
                if (
                    self.path.split("?", 1)[0] == "/pg/images/generations"
                    and is_image2_4k_model(request_json.get("model"))
                ):
                    response_request_json = dict(request_json)
                    response_request_json["response_format"] = "url"
                response_body, mode = transform_image_response(
                    response_request_json, content_type, response_body
                )
            except Exception:
                LOG.exception("failed to download or normalize upstream image")
                self.send_json_error(502, "failed to download upstream image")
                return
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
