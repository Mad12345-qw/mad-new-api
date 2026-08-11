import json
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, _format, *_args):
        return

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(length) or b"{}")
        if self.path.endswith("/responses/compact"):
            self.send_json(
                {
                    "id": "cmp_acceptance",
                    "object": "response.compaction",
                    "created_at": int(time.time()),
                    "output": [
                        {
                            "type": "compaction_summary",
                            "summary": "The remembered code word is ORCHID.",
                        }
                    ],
                    "usage": {
                        "input_tokens": 12,
                        "output_tokens": 4,
                        "total_tokens": 16,
                    },
                }
            )
            return
        if not self.path.endswith("/chat/completions"):
            self.send_error(404)
            return
        if body.get("stream"):
            chunks = [
                {
                    "id": "chatcmpl-reasoning",
                    "object": "chat.completion.chunk",
                    "created": int(time.time()),
                    "model": body.get("model", "gpt-5.6-luna"),
                    "choices": [
                        {
                            "index": 0,
                            "delta": {
                                "role": "assistant",
                                "reasoning_content": "I checked the arithmetic carefully.",
                            },
                            "finish_reason": None,
                        }
                    ],
                },
                {
                    "id": "chatcmpl-reasoning",
                    "object": "chat.completion.chunk",
                    "created": int(time.time()),
                    "model": body.get("model", "gpt-5.6-luna"),
                    "choices": [
                        {
                            "index": 0,
                            "delta": {"content": "42"},
                            "finish_reason": None,
                        }
                    ],
                },
                {
                    "id": "chatcmpl-reasoning",
                    "object": "chat.completion.chunk",
                    "created": int(time.time()),
                    "model": body.get("model", "gpt-5.6-luna"),
                    "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                    "usage": {
                        "prompt_tokens": 10,
                        "completion_tokens": 6,
                        "total_tokens": 16,
                        "completion_tokens_details": {"reasoning_tokens": 4},
                    },
                },
            ]
            encoded = b"".join(
                b"data: " + json.dumps(chunk, separators=(",", ":")).encode("ascii") + b"\n\n"
                for chunk in chunks
            ) + b"data: [DONE]\n\n"
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)
            return
        self.send_json(
            {
                "id": "chatcmpl-reasoning",
                "object": "chat.completion",
                "created": int(time.time()),
                "model": body.get("model", "gpt-5.6-luna"),
                "choices": [
                    {
                        "index": 0,
                        "message": {
                            "role": "assistant",
                            "reasoning_content": "I checked the arithmetic carefully.",
                            "content": "42",
                        },
                        "finish_reason": "stop",
                    }
                ],
                "usage": {"prompt_tokens": 10, "completion_tokens": 6, "total_tokens": 16},
            }
        )

    def send_json(self, payload):
        encoded = json.dumps(payload, separators=(",", ":")).encode("ascii")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)


ThreadingHTTPServer(("172.19.0.1", 18094), Handler).serve_forever()
