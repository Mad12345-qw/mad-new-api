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

        if body.get("stream"):
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
        except URLError:
            time.sleep(0.5)
    raise RuntimeError("native CPA container did not become healthy")


def run(command: list[str]) -> None:
    subprocess.run(command, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", required=True)
    args = parser.parse_args()

    MockMadAPIHandler.calls = []
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

        if b'"url":"https://example.invalid/image.png"' not in non_stream:
            raise RuntimeError("native image response was not relayed")
        if b"data:" not in stream or b"[DONE]" not in stream:
            raise RuntimeError("native image stream was not relayed")
        if len(MockMadAPIHandler.calls) != 2:
            raise RuntimeError(f"expected two MadAPI calls, got {len(MockMadAPIHandler.calls)}")
        for call in MockMadAPIHandler.calls:
            if call["path"] != "/v1/images/generations":
                raise RuntimeError(f"wrong upstream path: {call['path']}")
            if call["authorization"] != "Bearer native-runtime-acceptance":
                raise RuntimeError("caller authorization was not preserved")
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
