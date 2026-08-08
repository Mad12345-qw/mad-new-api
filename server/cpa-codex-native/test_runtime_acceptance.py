#!/usr/bin/env python3
"""Exercise the built CPA through its selected-channel internal endpoints."""

from __future__ import annotations

import argparse
import base64
import json
import socket
import struct
import subprocess
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import Request, urlopen


PNG_1X1 = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2nWQAAAAASUVORK5CYII="
)


class MockSelectedChannelHandler(BaseHTTPRequestHandler):
    calls: list[dict[str, object]] = []
    lock = threading.Lock()

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(length).decode("utf-8"))
        path = urlsplit(self.path).path
        with self.lock:
            self.calls.append(
                {
                    "path": path,
                    "authorization": self.headers.get("Authorization", ""),
                    "body": body,
                }
            )

        if path.endswith("/responses"):
            self._json(
                500,
                {
                    "error": {
                        "type": "tools_not_supported",
                        "message": "Tool/function calling is not supported by this proxy. Use chat completions without tools parameter.",
                    }
                },
            )
            return
        if path.endswith("/images/generations"):
            self._json(
                200,
                {
                    "created": 1,
                    "data": [
                        {
                            "b64_json": base64.b64encode(PNG_1X1).decode("ascii"),
                            "output_format": "png",
                        }
                    ],
                },
            )
            return
        if not path.endswith("/chat/completions"):
            self.send_error(404)
            return

        if not body.get("stream"):
            self._json(
                200,
                {
                    "id": "chatcmpl-nonstream",
                    "object": "chat.completion",
                    "created": 1,
                    "model": body.get("model", "deepseek-v4-flash"),
                    "choices": [
                        {
                            "index": 0,
                            "message": {
                                "role": "assistant",
                                "content": "<thinking>private trace</thinking>public answer",
                            },
                            "finish_reason": "stop",
                        }
                    ],
                    "usage": {"prompt_tokens": 8, "completion_tokens": 4, "total_tokens": 12},
                },
            )
            return

        has_tool_output = any(message.get("role") == "tool" for message in body.get("messages", []))
        if body.get("tools") and not has_tool_output:
            chunks = [
                {
                    "id": "chatcmpl-tools",
                    "object": "chat.completion.chunk",
                    "created": 1,
                    "model": body.get("model", "deepseek-v4-flash"),
                    "choices": [
                        {
                            "index": 0,
                            "delta": {
                                "role": "assistant",
                                "tool_calls": [
                                    {
                                        "index": 0,
                                        "id": "call_exact_failure",
                                        "type": "function",
                                        "function": {"name": "echo", "arguments": ""},
                                    }
                                ],
                            },
                            "finish_reason": None,
                        }
                    ],
                },
                {
                    "id": "chatcmpl-tools",
                    "object": "chat.completion.chunk",
                    "created": 1,
                    "model": body.get("model", "deepseek-v4-flash"),
                    "choices": [
                        {
                            "index": 0,
                            "delta": {
                                "tool_calls": [
                                    {
                                        "index": 0,
                                        "function": {"arguments": '{"text":"exact-error-fixed"}'},
                                    }
                                ]
                            },
                            "finish_reason": "tool_calls",
                        }
                    ],
                },
            ]
        else:
            chunks = [
                {
                    "id": "chatcmpl-text",
                    "object": "chat.completion.chunk",
                    "created": 1,
                    "model": body.get("model", "deepseek-v4-flash"),
                    "choices": [
                        {
                            "index": 0,
                            "delta": {
                                "role": "assistant",
                                "content": "<thinking>private trace</thinking>public answer",
                            },
                            "finish_reason": "stop",
                        }
                    ],
                }
            ]
        chunks.append(
            {
                "id": "chatcmpl-usage",
                "object": "chat.completion.chunk",
                "created": 1,
                "model": body.get("model", "deepseek-v4-flash"),
                "choices": [],
                "usage": {"prompt_tokens": 8, "completion_tokens": 4, "total_tokens": 12},
            }
        )
        payload = b"".join(
            b"data: " + json.dumps(chunk, separators=(",", ":")).encode("utf-8") + b"\n\n"
            for chunk in chunks
        ) + b"data: [DONE]\n\n"
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def _json(self, status: int, value: dict[str, object]) -> None:
        payload = json.dumps(value, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, _format: str, *_args: object) -> None:
        pass


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def request_bytes(url: str, body: dict[str, object], headers: dict[str, str]) -> tuple[int, bytes]:
    request = Request(
        url,
        data=json.dumps(body, separators=(",", ":")).encode("utf-8"),
        method="POST",
        headers={"Content-Type": "application/json", **headers},
    )
    try:
        with urlopen(request, timeout=30) as response:  # nosec B310 - localhost acceptance server
            return response.status, response.read()
    except HTTPError as error:
        return error.code, error.read()


def sse_events(payload: bytes) -> list[dict[str, object]]:
    events: list[dict[str, object]] = []
    for line in payload.decode("utf-8", errors="replace").splitlines():
        if not line.startswith("data:"):
            continue
        data = line[5:].strip()
        if not data or data == "[DONE]":
            continue
        events.append(json.loads(data))
    return events


def wait_for_health(port: int) -> None:
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        try:
            with urlopen(f"http://127.0.0.1:{port}/healthz", timeout=2) as response:  # nosec B310
                if response.status == 200:
                    return
        except (OSError, URLError):
            time.sleep(0.5)
    raise RuntimeError("selected-channel CPA container did not become healthy")


def dispatch_body(mock_port: int, body: dict[str, object], *, stream: bool = True) -> dict[str, object]:
    return {
        "channel_type": 43,
        "channel_id": 91,
        "wire_protocol": "openai_chat_completions",
        "base_url": f"http://host.docker.internal:{mock_port}/v1",
        "api_key": "selected-channel-secret",
        "model": "deepseek-v4-flash",
        "body": body,
        "stream": stream,
        "session_scope": "runtime-acceptance-scope",
        "first_event_timeout_seconds": 30,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", required=True)
    args = parser.parse_args()

    MockSelectedChannelHandler.calls = []
    mock_port = free_port()
    cpa_port = free_port()
    mock = ThreadingHTTPServer(("0.0.0.0", mock_port), MockSelectedChannelHandler)
    threading.Thread(target=mock.serve_forever, daemon=True).start()
    container = f"selected-channel-cpa-{uuid.uuid4().hex[:12]}"
    dispatch_token = "runtime-dispatch-secret"
    try:
        subprocess.run(
            [
                "docker",
                "run",
                "--detach",
                "--rm",
                "--name",
                container,
                "--add-host",
                "host.docker.internal:host-gateway",
                "--publish",
                f"127.0.0.1:{cpa_port}:8317",
                "--env",
                f"MADAPI_CODEX_DISPATCH_TOKEN={dispatch_token}",
                "--env",
                f"MADAPI_INTERNAL_URL=http://host.docker.internal:{mock_port}",
                args.image,
            ],
            check=True,
            stdout=subprocess.DEVNULL,
        )
        wait_for_health(cpa_port)
        execute_url = f"http://127.0.0.1:{cpa_port}/internal/madapi/codex/execute"
        internal_headers = {"X-MadAPI-Codex-Dispatch-Token": dispatch_token}
        tool = {
            "type": "function",
            "name": "echo",
            "description": "Echo text",
            "parameters": {
                "type": "object",
                "properties": {"text": {"type": "string"}},
                "required": ["text"],
                "additionalProperties": False,
            },
            "strict": True,
        }

        first_request = {
            "model": "deepseek-v4-flash",
            "input": "Call echo exactly once.",
            "tools": [tool],
            "tool_choice": "required",
            "stream": True,
        }
        status, payload = request_bytes(
            execute_url,
            dispatch_body(mock_port, first_request),
            internal_headers,
        )
        if status != 200:
            raise RuntimeError(f"tool dispatch returned HTTP {status}: {payload[:300]!r}")
        events = sse_events(payload)
        event_types = [str(event.get("type")) for event in events]
        arguments = "".join(
            str(event.get("delta", ""))
            for event in events
            if event.get("type") == "response.function_call_arguments.delta"
        )
        if "response.completed" not in event_types or arguments != '{"text":"exact-error-fixed"}':
            raise RuntimeError("selected-channel tool conversion failed")
        completed = next(event for event in events if event.get("type") == "response.completed")
        response_id = str((completed.get("response") or {}).get("id", ""))
        if not response_id:
            raise RuntimeError("tool response id is missing")

        continuation = {
            "model": "deepseek-v4-flash",
            "previous_response_id": response_id,
            "input": [
                {
                    "type": "function_call_output",
                    "call_id": "call_exact_failure",
                    "output": "ok",
                }
            ],
            "tools": [tool],
            "stream": True,
        }
        status, payload = request_bytes(
            execute_url,
            dispatch_body(mock_port, continuation),
            internal_headers,
        )
        continued_events = sse_events(payload)
        if status != 200 or not any(event.get("type") == "response.completed" for event in continued_events):
            raise RuntimeError("string-input session replay failed")

        thinking_request = {
            "model": "deepseek-v4-flash",
            "input": "Return the public answer.",
            "stream": True,
        }
        status, payload = request_bytes(
            execute_url,
            dispatch_body(mock_port, thinking_request),
            internal_headers,
        )
        thinking_text = payload.decode("utf-8", errors="replace")
        thinking_events = sse_events(payload)
        if status != 200 or "<thinking>" in thinking_text or "</thinking>" in thinking_text:
            raise RuntimeError("inline thinking tags leaked")
        if not any(event.get("type") == "response.reasoning_summary_text.delta" for event in thinking_events):
            raise RuntimeError("reasoning events are missing")
        public_text = "".join(
            str(event.get("delta", ""))
            for event in thinking_events
            if event.get("type") == "response.output_text.delta"
        )
        if public_text != "public answer":
            raise RuntimeError("public text was not preserved")

        nonstream_request = {
            "model": "deepseek-v4-flash",
            "input": "Return the public answer.",
            "stream": False,
        }
        status, payload = request_bytes(
            execute_url,
            dispatch_body(mock_port, nonstream_request, stream=False),
            internal_headers,
        )
        nonstream = json.loads(payload)
        if status != 200 or nonstream.get("object") != "response" or nonstream.get("status") != "completed":
            raise RuntimeError("non-streaming Responses conversion failed")
        if b"<thinking>" in payload or b"</thinking>" in payload:
            raise RuntimeError("non-streaming thinking tags leaked")

        image_url = f"http://127.0.0.1:{cpa_port}/internal/madapi/codex/image"
        image_request = {
            "model": "gpt-5.6-terra",
            "input": "Generate one test pixel.",
            "tools": [
                {
                    "type": "image_generation",
                    "action": "generate",
                    "model": "gpt-image-2",
                }
            ],
            "tool_choice": {"type": "image_generation"},
            "stream": False,
        }
        status, payload = request_bytes(
            image_url,
            image_request,
            {
                **internal_headers,
                "Authorization": "Bearer image-user-token",
            },
        )
        image_response = json.loads(payload)
        image_items = [item for item in image_response.get("output", []) if item.get("type") == "image_generation_call"]
        if status != 200 or len(image_items) != 1:
            raise RuntimeError("image_generation response item is missing")
        image_bytes = base64.b64decode(image_items[0].get("result", ""), validate=True)
        if image_bytes != PNG_1X1 or struct.unpack(">II", image_bytes[16:24]) != (1, 1):
            raise RuntimeError("image_generation result is not the expected PNG")

        paths = [str(call["path"]) for call in MockSelectedChannelHandler.calls]
        if any(path.endswith("/responses") for path in paths):
            raise RuntimeError("selected-channel execution called the rejected Responses endpoint")
        if paths.count("/v1/chat/completions") != 4:
            raise RuntimeError(f"unexpected chat call count: {paths}")
        if paths.count("/v1/images/generations") != 1:
            raise RuntimeError(f"unexpected image call count: {paths}")
        chat_calls = [call for call in MockSelectedChannelHandler.calls if call["path"] == "/v1/chat/completions"]
        if any(call["authorization"] != "Bearer selected-channel-secret" for call in chat_calls):
            raise RuntimeError("selected channel credential was not used")
        image_call = next(call for call in MockSelectedChannelHandler.calls if call["path"] == "/v1/images/generations")
        if image_call["authorization"] != "Bearer image-user-token":
            raise RuntimeError("image request user authorization was not preserved")
    finally:
        subprocess.run(
            ["docker", "rm", "--force", container],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        mock.shutdown()
        mock.server_close()

    print("selected-channel CPA runtime acceptance passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
