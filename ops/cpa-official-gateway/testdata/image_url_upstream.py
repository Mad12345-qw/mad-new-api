import json
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


PNG = bytes.fromhex(
    "89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c489"
    "0000000d49444154789c6360f8cfc0000004010100f57f65890000000049454e44ae426082"
)


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, _format, *_args):
        return

    def do_GET(self):
        if self.path != "/image.png":
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Type", "image/png")
        self.send_header("Content-Length", str(len(PNG)))
        self.end_headers()
        self.wfile.write(PNG)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(length)
        if not self.path.endswith("/images/generations"):
            self.send_error(404)
            return
        payload = {
            "created": int(time.time()),
            "data": [{"url": "http://172.19.0.1:18093/image.png"}],
            "usage": {
                "input_tokens": 10,
                "output_tokens": 0,
                "total_tokens": 10,
            },
        }
        encoded = json.dumps(payload, separators=(",", ":")).encode("ascii")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)


ThreadingHTTPServer(("172.19.0.1", 18093), Handler).serve_forever()
