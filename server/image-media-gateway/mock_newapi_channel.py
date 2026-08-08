#!/usr/bin/env python3
import base64
import json
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


MODE = os.getenv("MOCK_MODE", "success")
DELAY_SECONDS = float(os.getenv("MOCK_DELAY_SECONDS", "0"))
FAIL_STATUS = int(os.getenv("MOCK_STATUS", "520"))
IMAGE_BYTES = int(os.getenv("MOCK_IMAGE_BYTES", str(32 * 1024 * 1024)))
LOG_PATH = Path(os.getenv("MOCK_LOG_PATH", "/data/requests.jsonl"))
PNG_PREFIX = b"\x89PNG\r\n\x1a\n"
LOCK = threading.Lock()


def compact(value):
    return json.dumps(value, separators=(",", ":")).encode("ascii")


def write_base64_image(handle, size):
    remaining = size
    first = True
    while remaining:
        count = min(65535, remaining)
        if remaining > count:
            count -= count % 3
        if first:
            raw = (PNG_PREFIX + b"\0" * max(0, count - len(PNG_PREFIX)))[:count]
            first = False
        else:
            raw = b"\0" * count
        handle.write(base64.b64encode(raw))
        remaining -= count


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_args):
        return

    def record(self, body):
        item = {
            "time": time.time(),
            "mode": MODE,
            "method": self.command,
            "path": self.path,
            "authorization": bool(self.headers.get("Authorization")),
            "x_goog_api_key": bool(self.headers.get("x-goog-api-key")),
            "body_bytes": len(body),
        }
        with LOCK:
            LOG_PATH.parent.mkdir(parents=True, exist_ok=True)
            with LOG_PATH.open("a", encoding="ascii") as handle:
                handle.write(json.dumps(item, separators=(",", ":")) + "\n")

    def send_json(self, status, value):
        payload = compact(value)
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        if self.path == "/health":
            self.send_json(200, {"ok": True, "mode": MODE})
            return
        if self.path.startswith("/asset/"):
            self.send_response(200)
            self.send_header("Content-Type", "image/png")
            self.send_header("Content-Length", str(IMAGE_BYTES))
            self.end_headers()
            remaining = IMAGE_BYTES
            first = True
            while remaining:
                count = min(65536, remaining)
                if first:
                    chunk = (PNG_PREFIX + b"\0" * max(0, count - len(PNG_PREFIX)))[:count]
                    first = False
                else:
                    chunk = b"\0" * count
                self.wfile.write(chunk)
                remaining -= count
            return
        self.send_json(404, {"error": {"message": "not found"}})

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0") or "0")
        body = self.rfile.read(length) if length else b""
        self.record(body)
        if DELAY_SECONDS:
            time.sleep(DELAY_SECONDS)
        if MODE == "fail":
            self.send_json(
                FAIL_STATUS,
                {
                    "error": {
                        "message": "The origin web server sent a response that Cloudflare could not parse.",
                        "type": "mock_cloudflare_520",
                    }
                },
            )
            return
        if MODE == "protocol400":
            self.send_json(400, {"error": {"message": 'json: unknown field "safetySettings"'}})
            return
        if MODE == "disconnect":
            self.close_connection = True
            self.connection.close()
            return
        if MODE == "empty":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if MODE == "malformed":
            payload = b'{"candidates":['
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return
        if MODE == "url":
            host = self.headers.get("Host", "madapi-gemini-success:9000")
            self.send_json(
                200,
                {
                    "created": int(time.time()),
                    "data": [{"url": "http://" + host + "/asset/" + str(time.time_ns())}],
                    "usage": {"input_tokens": 8, "output_tokens": 1, "total_tokens": 9},
                },
            )
            return

        prefix = b'{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"'
        suffix = b'"}}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1400,"totalTokenCount":1408}}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(prefix)
        write_base64_image(self.wfile, IMAGE_BYTES)
        self.wfile.write(suffix)
        self.close_connection = True


def main():
    port = int(os.getenv("MOCK_PORT", "9000"))
    ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
