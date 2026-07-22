import base64
import importlib.util
import json
import tempfile
import threading
import unittest
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("service.py")
SPEC = importlib.util.spec_from_file_location("image_url_compat", MODULE_PATH)
service = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(service)

PNG_BYTES = b"\x89PNG\r\n\x1a\n" + b"test-image"
PNG_B64 = base64.b64encode(PNG_BYTES).decode("ascii")


class MockUpstreamHandler(BaseHTTPRequestHandler):
    last_request = None

    def log_message(self, _fmt, *_args):
        return

    def do_OPTIONS(self):
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_HEAD(self):
        self.send_response(200)
        self.send_header("Content-Type", "image/png")
        self.send_header("Content-Length", str(len(PNG_BYTES)))
        self.end_headers()

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "image/png")
        self.send_header("Content-Length", str(len(PNG_BYTES)))
        self.end_headers()
        self.wfile.write(PNG_BYTES)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        type(self).last_request = json.loads(self.rfile.read(length))
        if type(self).last_request.get("response_format") == "url":
            item = {
                "url": f"http://127.0.0.1:{self.server.server_port}/generated.png"
            }
        else:
            item = {"b64_json": PNG_B64}
        body = json.dumps({"data": [item]}).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class ImageURLCompatTest(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        service.CACHE_DIR = Path(self.temp_dir.name)
        service.PUBLIC_BASE_URL = "https://mad.example"
        MockUpstreamHandler.last_request = None

    def tearDown(self):
        self.temp_dir.cleanup()

    def response(self):
        return json.dumps({"data": [{"b64_json": PNG_B64}]}).encode("utf-8")

    def test_standard_image2_forces_base64_upstream(self):
        original = json.dumps(
            {"model": "gpt-image-2", "prompt": "test", "response_format": "url"}
        ).encode("utf-8")
        request_json, upstream_body = service.prepare_upstream_body(original)
        self.assertEqual(request_json["response_format"], "url")
        self.assertEqual(json.loads(upstream_body)["response_format"], "b64_json")

    def test_4k_image2_forces_url_upstream(self):
        original = json.dumps(
            {
                "model": "gpt-image-2-4k",
                "prompt": "test",
                "response_format": "b64_json",
            }
        ).encode("utf-8")
        request_json, upstream_body = service.prepare_upstream_body(original)
        self.assertEqual(request_json["response_format"], "b64_json")
        self.assertEqual(json.loads(upstream_body)["response_format"], "url")

    def test_explicit_base64_returns_base64_only(self):
        body, mode = service.transform_image_response(
            {"model": "gpt-image-2-4k", "response_format": "b64_json"},
            "application/json",
            self.response(),
        )
        item = json.loads(body)["data"][0]
        self.assertEqual(mode, "b64_json")
        self.assertEqual(item["b64_json"], PNG_B64)
        self.assertNotIn("url", item)

    def test_explicit_url_returns_temporary_url_only(self):
        body, mode = service.transform_image_response(
            {"model": "gpt-image-2-4k", "response_format": "url"},
            "application/json",
            self.response(),
        )
        item = json.loads(body)["data"][0]
        self.assertEqual(mode, "url")
        self.assertTrue(item["url"].startswith("https://mad.example/image-cache/"))
        self.assertNotIn("b64_json", item)
        self.assertEqual(len(list(service.CACHE_DIR.glob("*.png"))), 1)

    def test_omitted_format_preserves_base64_and_adds_url(self):
        body, mode = service.transform_image_response(
            {"model": "gpt-image-2"}, "application/json", self.response()
        )
        item = json.loads(body)["data"][0]
        self.assertEqual(mode, "dual")
        self.assertEqual(item["b64_json"], PNG_B64)
        self.assertTrue(item["url"].startswith("https://mad.example/image-cache/"))

    def test_other_models_are_unchanged(self):
        original = json.dumps({"model": "other-image", "response_format": "url"}).encode(
            "utf-8"
        )
        request_json, upstream_body = service.prepare_upstream_body(original)
        self.assertEqual(request_json["model"], "other-image")
        self.assertEqual(upstream_body, original)

    def test_http_handler_downloads_4k_url_and_supports_both_formats(self):
        upstream = ThreadingHTTPServer(("127.0.0.1", 0), MockUpstreamHandler)
        upstream_thread = threading.Thread(target=upstream.serve_forever, daemon=True)
        upstream_thread.start()
        previous_upstream = service.UPSTREAM
        service.UPSTREAM = f"http://127.0.0.1:{upstream.server_port}"
        compat = ThreadingHTTPServer(("127.0.0.1", 0), service.Handler)
        compat_thread = threading.Thread(target=compat.serve_forever, daemon=True)
        compat_thread.start()
        endpoint = f"http://127.0.0.1:{compat.server_port}/pg/images/generations"
        try:
            request = urllib.request.Request(
                endpoint,
                data=json.dumps(
                    {
                        "model": "gpt-image-2-4k",
                        "prompt": "test",
                        "response_format": "url",
                    }
                ).encode("utf-8"),
                headers={"Content-Type": "application/json"},
                method="POST",
            )
            with urllib.request.urlopen(request) as response:
                payload = json.load(response)
                self.assertEqual(response.headers["X-Image-URL-Compat"], "url")
            self.assertEqual(
                MockUpstreamHandler.last_request["response_format"], "url"
            )
            self.assertIn("url", payload["data"][0])
            self.assertNotIn("b64_json", payload["data"][0])

            base64_request = urllib.request.Request(
                endpoint,
                data=json.dumps(
                    {
                        "model": "gpt-image-2-4k",
                        "prompt": "test",
                        "response_format": "b64_json",
                    }
                ).encode("utf-8"),
                headers={"Content-Type": "application/json"},
                method="POST",
            )
            with urllib.request.urlopen(base64_request) as response:
                base64_payload = json.load(response)
                self.assertEqual(response.headers["X-Image-URL-Compat"], "b64_json")
            self.assertEqual(
                MockUpstreamHandler.last_request["response_format"], "url"
            )
            self.assertEqual(base64_payload["data"][0]["b64_json"], PNG_B64)
            self.assertNotIn("url", base64_payload["data"][0])

            options = urllib.request.Request(endpoint, method="OPTIONS")
            with urllib.request.urlopen(options) as response:
                self.assertEqual(response.status, 204)
                self.assertEqual(response.headers["Access-Control-Allow-Origin"], "*")
        finally:
            compat.shutdown()
            compat.server_close()
            upstream.shutdown()
            upstream.server_close()
            service.UPSTREAM = previous_upstream


if __name__ == "__main__":
    unittest.main()
