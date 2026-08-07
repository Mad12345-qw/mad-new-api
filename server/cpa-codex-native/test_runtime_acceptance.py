#!/usr/bin/env python3
"""Exercise the built native CPA image through its public image endpoints."""

from __future__ import annotations

import argparse
import json
import socket
import subprocess
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import URLError
from urllib.request import Request, urlopen


class MockMadAPIHandler(BaseHTTPRequestHandler):
    calls: list[dict[str, object]] = []
    calls_lock = threading.Lock()
    bootstrap_attempts = 0

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(length).decode("utf-8"))
        with self.calls_lock:
            self.calls.append(
                {
                    "path": self.path,
                    "authorization": self.headers.get("Authorization", ""),
                    "body": body,
                }
            )

        if self.path == "/v1/responses":
            serialized = json.dumps(body, ensure_ascii=True)
            if "bootstrap retry" in serialized:
                with self.calls_lock:
                    type(self).bootstrap_attempts += 1
                    attempt = type(self).bootstrap_attempts
                if attempt == 1:
                    payload = b""
                else:
                    payload = b"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"
            elif any(tool.get("type") == "function" for tool in body.get("tools", [])):
                payload = (
                    b"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\"}}\n\n"
                    b"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{}\"}\n\n"
                    b"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\"}\n\n"
                    b"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"
                )
            elif "native image" in serialized:
                payload = (
                    b"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"image_generation_call\"}}\n\n"
                    b"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"mock-image\"}}\n\n"
                    b"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"
                )
            else:
                payload = (
                    b"event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"native reasoning\"}\n\n"
                    b"event: response.reasoning_summary_text.done\ndata: {\"type\":\"response.reasoning_summary_text.done\"}\n\n"
                    b"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n"
                    b"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"
                )
            content_type = "text/event-stream"
        elif self.path == "/v1/chat/completions":
            serialized = json.dumps(body, ensure_ascii=True)
            if "bootstrap retry" in serialized:
                with self.calls_lock:
                    type(self).bootstrap_attempts += 1
                    attempt = type(self).bootstrap_attempts
                if attempt == 1:
                    payload = b""
                    content_type = "text/event-stream"
                else:
                    chunks = [
                        {
                            "id": "mock-bootstrap",
                            "object": "chat.completion.chunk",
                            "created": 1,
                            "model": "gpt-5.6-terra",
                            "choices": [{"index": 0, "delta": {"content": "recovered"}, "finish_reason": "stop"}],
                        }
                    ]
                    payload = b"".join(
                        b"data: " + json.dumps(chunk, separators=(",", ":")).encode("utf-8") + b"\n\n"
                        for chunk in chunks
                    ) + b"data: [DONE]\n\n"
                    content_type = "text/event-stream"
            elif "inline thinking" in serialized:
                chunks = [
                    {
                        "id": "mock-inline-thinking",
                        "object": "chat.completion.chunk",
                        "created": 1,
                        "model": "gpt-5.6-luna",
                        "choices": [
                            {"index": 0, "delta": {"role": "assistant", "content": "<think"}, "finish_reason": None}
                        ],
                    },
                    {
                        "id": "mock-inline-thinking",
                        "object": "chat.completion.chunk",
                        "created": 1,
                        "model": "gpt-5.6-luna",
                        "choices": [
                            {
                                "index": 0,
                                "delta": {"content": "ing>private trace</thinking>public answer"},
                                "finish_reason": "stop",
                            }
                        ],
                    },
                ]
                payload = b"".join(
                    b"data: " + json.dumps(chunk, separators=(",", ":")).encode("utf-8") + b"\n\n"
                    for chunk in chunks
                ) + b"data: [DONE]\n\n"
                content_type = "text/event-stream"
            elif body.get("tools"):
                chunks = [
                    {
                        "id": "mock-tool",
                        "object": "chat.completion.chunk",
                        "created": 1,
                        "model": "gpt-5.6-terra",
                        "choices": [
                            {
                                "index": 0,
                                "delta": {
                                    "role": "assistant",
                                    "tool_calls": [
                                        {
                                            "index": 0,
                                            "id": "call_lookup",
                                            "type": "function",
                                            "function": {"name": "lookup", "arguments": ""},
                                        }
                                    ],
                                },
                                "finish_reason": None,
                            }
                        ],
                    },
                    {
                        "id": "mock-tool",
                        "object": "chat.completion.chunk",
                        "created": 1,
                        "model": "gpt-5.6-terra",
                        "choices": [
                            {
                                "index": 0,
                                "delta": {"tool_calls": [{"index": 0, "function": {"arguments": "{}"}}]},
                                "finish_reason": "tool_calls",
                            }
                        ],
                    },
                ]
                payload = b"".join(
                    b"data: " + json.dumps(chunk, separators=(",", ":")).encode("utf-8") + b"\n\n"
                    for chunk in chunks
                ) + b"data: [DONE]\n\n"
                content_type = "text/event-stream"
            else:
                chunks = [
                    {
                        "id": "mock-chat",
                        "object": "chat.completion.chunk",
                        "created": 1,
                        "model": "gpt-5.6-terra",
                        "choices": [
                            {
                                "index": 0,
                                "delta": {"role": "assistant", "reasoning_content": "mock reasoning"},
                                "finish_reason": None,
                            }
                        ],
                    },
                    {
                        "id": "mock-chat",
                        "object": "chat.completion.chunk",
                        "created": 1,
                        "model": "gpt-5.6-terra",
                        "choices": [
                            {"index": 0, "delta": {"content": "OK"}, "finish_reason": "stop"}
                        ],
                    },
                ]
                payload = b"".join(
                    b"data: " + json.dumps(chunk, separators=(",", ":")).encode("utf-8") + b"\n\n"
                    for chunk in chunks
                ) + b"data: [DONE]\n\n"
                content_type = "text/event-stream"
        elif body.get("stream"):
            payload = b'data: {"type":"image_generation.partial_image","url":"https://example.invalid/partial.png"}\n\ndata: [DONE]\n\n'
            content_type = "text/event-stream"
        else:
            payload = b'{"created":1,"data":[{"url":"https://example.invalid/image.png"}]}'
            content_type = "application/json"

        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, _format: str, *_args: object) -> None:
        pass


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def request_bytes(url: str, body: dict[str, object]) -> bytes:
    request = Request(
        url,
        data=json.dumps(body).encode("utf-8"),
        method="POST",
        headers={
            "Authorization": "Bearer native-runtime-acceptance",
            "Content-Type": "application/json",
        },
    )
    with urlopen(request, timeout=30) as response:  # nosec B310 - localhost test server
        return response.read()


def wait_for_health(port: int) -> None:
    deadline = time.monotonic() + 60
    url = f"http://127.0.0.1:{port}/healthz"
    while time.monotonic() < deadline:
        try:
            with urlopen(url, timeout=2) as response:  # nosec B310 - localhost test server
                if response.status == 200:
                    return
        except (OSError, URLError):
            time.sleep(0.5)
    raise RuntimeError("native CPA container did not become healthy")


def run(command: list[str]) -> None:
    subprocess.run(command, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", required=True)
    args = parser.parse_args()

    MockMadAPIHandler.calls = []
    MockMadAPIHandler.bootstrap_attempts = 0
    mock_port = free_port()
    cpa_port = free_port()
    server = ThreadingHTTPServer(("0.0.0.0", mock_port), MockMadAPIHandler)
    server_thread = threading.Thread(target=server.serve_forever, daemon=True)
    server_thread.start()

    container = f"native-cpa-runtime-{uuid.uuid4().hex[:12]}"
    try:
        run(
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
                f"MADAPI_INTERNAL_URL=http://host.docker.internal:{mock_port}",
                args.image,
            ]
        )
        wait_for_health(cpa_port)

        image_url = f"http://127.0.0.1:{cpa_port}/v1/images/generations"
        non_stream = request_bytes(image_url, {"model": "gpt-image-2", "prompt": "test image"})
        stream = request_bytes(image_url, {"model": "gpt-image-2", "prompt": "test stream", "stream": True})
        responses = request_bytes(
            f"http://127.0.0.1:{cpa_port}/v1/responses",
            {"model": "gpt-5.6-terra", "input": "test reasoning", "stream": True},
        )
        inline_thinking = request_bytes(
            f"http://127.0.0.1:{cpa_port}/v1/responses",
            {"model": "gpt-5.6-luna", "input": "test inline thinking", "stream": True},
        )
        tools = request_bytes(
            f"http://127.0.0.1:{cpa_port}/v1/responses",
            {
                "model": "gpt-5.6-terra",
                "input": "test tool",
                "stream": True,
                "reasoning": {"effort": "medium"},
                "tools": [{"type": "function", "name": "lookup", "parameters": {"type": "object"}}],
            },
        )
        native_image = request_bytes(
            f"http://127.0.0.1:{cpa_port}/v1/responses",
            {
                "model": "gpt-5.6-luna",
                "input": "test native image",
                "stream": True,
                "tools": [
                    {
                        "type": "image_generation",
                        "action": "generate",
                        "model": "gpt-image-2",
                    }
                ],
            },
        )
        bootstrap = request_bytes(
            f"http://127.0.0.1:{cpa_port}/v1/responses",
            {"model": "gpt-5.6-terra", "input": "bootstrap retry", "stream": True},
        )

        if b'"url":"https://example.invalid/image.png"' not in non_stream:
            raise RuntimeError("native image response was not relayed")
        if b"data:" not in stream or b"[DONE]" not in stream:
            raise RuntimeError("native image stream was not relayed")
        for required_event in (
            b"response.reasoning_summary_text.delta",
            b"response.reasoning_summary_text.done",
            b"response.output_text.delta",
            b"response.completed",
        ):
            if required_event not in responses:
                raise RuntimeError(f"missing native Responses event: {required_event.decode('ascii')}")
        if b"[reasoning unavailable]" in responses:
            raise RuntimeError("native Responses stream leaked a reasoning placeholder")
        if b"<thinking>" in inline_thinking or b"</thinking>" in inline_thinking:
            raise RuntimeError("inline thinking tags leaked into the native Responses stream")
        for required_event in (
            b"response.reasoning_summary_text.delta",
            b"response.reasoning_summary_text.done",
            b"response.output_text.delta",
            b"response.completed",
        ):
            if required_event not in inline_thinking:
                raise RuntimeError(f"missing inline-thinking Responses event: {required_event.decode('ascii')}")
        for required_event in (
            b"response.output_item.added",
            b"response.function_call_arguments.delta",
            b"response.function_call_arguments.done",
            b"response.completed",
        ):
            if required_event not in tools:
                raise RuntimeError(f"missing native tool event: {required_event.decode('ascii')}")
        if b"image_generation_call" not in native_image or b"response.completed" not in native_image:
            raise RuntimeError("native Responses image tool was not returned to Codex")
        if b"response.completed" not in bootstrap:
            raise RuntimeError("bootstrap retry did not finish the Responses stream")
        if MockMadAPIHandler.bootstrap_attempts != 2:
            raise RuntimeError(f"expected one pre-event bootstrap retry, got {MockMadAPIHandler.bootstrap_attempts} attempts")
        if len(MockMadAPIHandler.calls) != 8:
            raise RuntimeError(f"expected eight MadAPI calls, got {len(MockMadAPIHandler.calls)}")
        for call in MockMadAPIHandler.calls:
            if call["authorization"] != "Bearer native-runtime-acceptance":
                raise RuntimeError("caller authorization was not preserved")
        for call in MockMadAPIHandler.calls[:2]:
            if call["path"] != "/v1/images/generations":
                raise RuntimeError(f"wrong upstream path: {call['path']}")
        for call in MockMadAPIHandler.calls[2:]:
            if call["path"] != "/v1/responses":
                raise RuntimeError(f"wrong Responses upstream path: {call['path']}")
        if not any(
            tool.get("type") == "image_generation"
            for tool in MockMadAPIHandler.calls[5]["body"].get("tools", [])
        ):
            raise RuntimeError("explicit native image tool was not forwarded")
        if MockMadAPIHandler.calls[1]["body"] != {"model": "gpt-image-2", "prompt": "test stream", "stream": True}:
            raise RuntimeError("stream image request body changed unexpectedly")
    finally:
        subprocess.run(["docker", "rm", "--force", container], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        server.shutdown()
        server.server_close()

    print("native CPA runtime image acceptance passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
