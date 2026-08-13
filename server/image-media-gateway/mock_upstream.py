import argparse
import base64
import cgi
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


PNG_PREFIX = b"\x89PNG\r\n\x1a\n"


class State:
    lock = threading.Lock()
    requests = 0
    url_requests = 0
    inline_requests = 0


def write_raw_image(handle, size):
    remaining = size
    first = True
    while remaining:
        count = min(65535, remaining)
        if first:
            chunk = (PNG_PREFIX + b"\0" * max(0, count - len(PNG_PREFIX)))[:count]
            first = False
        else:
            chunk = b"\0" * count
        handle.write(chunk)
        remaining -= count


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
    url_bytes = 3 * 1024 * 1024
    inline_bytes = 32 * 1024 * 1024
    delay = 0.02

    def log_message(self, *_args):
        return

    def do_GET(self):
        if self.path == "/stats":
            with State.lock:
                payload = json.dumps({
                    "requests": State.requests,
                    "url_requests": State.url_requests,
                    "inline_requests": State.inline_requests,
                }).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return
        if self.path.startswith("/asset/"):
            self.send_response(200)
            self.send_header("Content-Type", "image/png")
            self.send_header("Content-Length", str(self.url_bytes))
            self.end_headers()
            write_raw_image(self.wfile, self.url_bytes)
            return
        self.send_error(404)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        content_type = self.headers.get("Content-Type", "")
        raw = self.rfile.read(length)
        model = ""
        response_format = ""
        if content_type.startswith("application/json"):
            try:
                payload = json.loads(raw)
                model = str(payload.get("model") or "")
                response_format = str(payload.get("response_format") or "")
            except Exception:
                pass
        elif content_type.startswith("multipart/"):
            env = {"REQUEST_METHOD": "POST", "CONTENT_TYPE": content_type, "CONTENT_LENGTH": str(length)}
            form = cgi.FieldStorage(fp=__import__("io").BytesIO(raw), environ=env, keep_blank_values=True)
            model = form.getfirst("model", "")
            response_format = form.getfirst("response_format", "")
        inline = self.path.endswith("/chat/completions") or model.startswith("gemini-")
        with State.lock:
            State.requests += 1
            if inline:
                State.inline_requests += 1
            if response_format == "url":
                State.url_requests += 1
        time.sleep(self.delay)
        if inline:
            prefix = b'{"choices":[{"message":{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,'
            suffix = b'"}}]}}]}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(prefix)
            write_base64_image(self.wfile, self.inline_bytes)
            self.wfile.write(suffix)
            self.close_connection = True
            return
        host = self.headers.get("Host", "127.0.0.1:19080")
        payload = json.dumps({"created": int(time.time()), "data": [{"url": f"http://{host}/asset/{time.time_ns()}"}]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--listen", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=19080)
    parser.add_argument("--url-bytes", type=int, default=3 * 1024 * 1024)
    parser.add_argument("--inline-bytes", type=int, default=32 * 1024 * 1024)
    parser.add_argument("--delay", type=float, default=0.02)
    args = parser.parse_args()
    Handler.url_bytes = args.url_bytes
    Handler.inline_bytes = args.inline_bytes
    Handler.delay = args.delay
    ThreadingHTTPServer((args.listen, args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
