import base64
import importlib.util
import json
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("service.py")
SPEC = importlib.util.spec_from_file_location("image_url_compat", MODULE_PATH)
service = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(service)

PNG_BYTES = b"\x89PNG\r\n\x1a\n" + b"test-image"
PNG_B64 = base64.b64encode(PNG_BYTES).decode("ascii")


def multipart_body(fields, files):
    boundary = "----mad-image-compat-test"
    chunks = []
    for name, value in fields.items():
        chunks.extend(
            [
                f"--{boundary}\r\n".encode("ascii"),
                f'Content-Disposition: form-data; name="{name}"\r\n\r\n'.encode(
                    "ascii"
                ),
                str(value).encode("utf-8"),
                b"\r\n",
            ]
        )
    for name, filename, mime_type, raw in files:
        chunks.extend(
            [
                f"--{boundary}\r\n".encode("ascii"),
                (
                    f'Content-Disposition: form-data; name="{name}"; '
                    f'filename="{filename}"\r\n'
                ).encode("ascii"),
                f"Content-Type: {mime_type}\r\n\r\n".encode("ascii"),
                raw,
                b"\r\n",
            ]
        )
    chunks.append(f"--{boundary}--\r\n".encode("ascii"))
    return b"".join(chunks), f"multipart/form-data; boundary={boundary}"


class MockUpstreamHandler(BaseHTTPRequestHandler):
    last_request = None
    last_path = None

    def log_message(self, _fmt, *_args):
        return

    def do_OPTIONS(self):
        type(self).last_path = self.path
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
        if self.path.startswith("/v1/videos/"):
            task_id = self.path.rsplit("/", 1)[-1]
            body = json.dumps(
                {
                    "id": task_id,
                    "status": "completed",
                    "metadata": {"url": "https://media.example/result.mp4"},
                }
            ).encode("utf-8")
            type(self).last_path = self.path
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(200)
        self.send_header("Content-Type", "image/png")
        self.send_header("Content-Length", str(len(PNG_BYTES)))
        self.end_headers()
        self.wfile.write(PNG_BYTES)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        type(self).last_request = json.loads(self.rfile.read(length))
        type(self).last_path = self.path
        if self.path in {"/v1/video/generations", "/pg/videos"}:
            if type(self).last_request.get("model") == "force-404":
                body = b'{"error":{"message":"upstream route missing"}}'
                self.send_response(404)
            else:
                body = b'{"id":"task_seedance","status":"queued"}'
                self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self.path.endswith("/chat/completions"):
            body = json.dumps(
                {
                    "choices": [
                        {
                            "message": {
                                "content": (
                                    "Image generated\n"
                                    f"![image](data:image/png;base64,{PNG_B64})"
                                )
                            }
                        }
                    ]
                }
            ).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
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
        MockUpstreamHandler.last_path = None

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

    def test_image_aliases_map_to_canonical_paths(self):
        generation = json.dumps(
            {"model": "gpt-image-2", "prompt": "alias test"}
        ).encode("utf-8")
        for path in service.IMAGE_GENERATION_PATHS:
            _request, upstream_path, _body, _content_type = (
                service.prepare_upstream_request(path, generation)
            )
            expected = (
                "/pg/images/generations"
                if path.startswith("/pg/")
                else "/v1/images/generations"
            )
            self.assertEqual(upstream_path, expected, path)

        edit_body, edit_content_type = multipart_body(
            {"model": "gpt-image-2", "prompt": "edit alias"},
            [("image", "input.png", "image/png", PNG_BYTES)],
        )
        for path in service.IMAGE_EDIT_PATHS:
            _request, upstream_path, _body, _upstream_content_type = (
                service.prepare_upstream_request(path, edit_body, edit_content_type)
            )
            expected = (
                "/pg/images/edits"
                if path.startswith("/pg/")
                else "/v1/images/edits"
            )
            self.assertEqual(upstream_path, expected, path)

    def test_gemini_image_request_uses_chat_completions(self):
        original = json.dumps(
            {
                "model": "gemini-3-pro-image-preview",
                "prompt": "golden city",
                "size": "2048x1536",
                "aspect_ratio": "4:3",
                "response_format": "b64_json",
                "group": "vip",
            }
        ).encode("utf-8")
        request_json, path, upstream_body, content_type = service.prepare_upstream_request(
            "/v1/images/generations", original
        )
        payload = json.loads(upstream_body)
        self.assertEqual(request_json["model"], "gemini-3-pro-image-preview")
        self.assertEqual(path, "/v1/chat/completions")
        self.assertEqual(content_type, "application/json")
        self.assertEqual(payload["messages"][0]["content"], "golden city")
        self.assertEqual(payload["group"], "vip")
        self.assertEqual(
            payload["extra_body"]["google"]["image_config"],
            {"image_size": "2K", "aspect_ratio": "4:3"},
        )

    def test_gemini_json_reference_image_uses_multimodal_chat(self):
        original = json.dumps(
            {
                "model": "gemini-3.1-flash-image-preview",
                "prompt": "restyle this image",
                "image": "data:image/png;base64," + PNG_B64,
                "response_format": "url",
            }
        ).encode("utf-8")
        _request_json, path, upstream_body, content_type = (
            service.prepare_upstream_request("/images/generations", original)
        )
        payload = json.loads(upstream_body)
        content = payload["messages"][0]["content"]
        self.assertEqual(path, "/v1/chat/completions")
        self.assertEqual(content_type, "application/json")
        self.assertEqual(content[0]["text"], "restyle this image")
        self.assertEqual(
            content[1]["image_url"]["url"], "data:image/png;base64," + PNG_B64
        )

    def test_gemini_image_edit_multipart_uses_multimodal_chat(self):
        original, content_type = multipart_body(
            {
                "model": "gemini-3.1-flash-image-preview",
                "prompt": "change the sky to gold",
                "size": "2048x1536",
                "response_format": "url",
                "group": "vip",
            },
            [("image", "input.png", "image/png", PNG_BYTES)],
        )
        request_json, path, upstream_body, upstream_content_type = (
            service.prepare_upstream_request(
                "/v1/images/edits", original, content_type
            )
        )
        payload = json.loads(upstream_body)
        content = payload["messages"][0]["content"]
        self.assertEqual(request_json["response_format"], "url")
        self.assertEqual(path, "/v1/chat/completions")
        self.assertEqual(upstream_content_type, "application/json")
        self.assertEqual(content[0]["text"], "change the sky to gold")
        self.assertTrue(
            content[1]["image_url"]["url"].startswith(
                "data:image/png;base64,"
            )
        )
        self.assertEqual(payload["group"], "vip")
        self.assertEqual(
            payload["extra_body"]["google"]["image_config"]["image_size"],
            "2K",
        )

    def test_non_gemini_image_edit_multipart_is_unchanged(self):
        original, content_type = multipart_body(
            {"model": "gpt-image-2", "prompt": "keep passthrough"},
            [("image", "input.png", "image/png", PNG_BYTES)],
        )
        request_json, path, upstream_body, upstream_content_type = (
            service.prepare_upstream_request(
                "/v1/images/edits", original, content_type
            )
        )
        self.assertEqual(request_json["model"], "gpt-image-2")
        self.assertEqual(path, "/v1/images/edits")
        self.assertEqual(upstream_body, original)
        self.assertEqual(upstream_content_type, content_type)

    def test_openai_style_video_request_is_normalized(self):
        _original, normalized = service.normalize_video_request(
            json.dumps(
                {
                    "model": "doubao-seedance-2.0-cf-1080p",
                    "group": "vip",
                    "prompt": "animate the skyline",
                    "image_url": "data:image/png;base64," + PNG_B64,
                    "reference_image_urls": ["https://media.example/ref.png"],
                    "aspect_ratio": "9:16",
                    "resolution": "1080p",
                    "duration": 4,
                    "audio": True,
                }
            ).encode("utf-8")
        )
        payload = json.loads(normalized)
        self.assertEqual(payload["group"], "vip")
        self.assertEqual(payload["duration"], 4)
        self.assertEqual(payload["seconds"], "4")
        self.assertEqual(len(payload["images"]), 2)
        self.assertEqual(payload["metadata"]["ratio"], "9:16")
        self.assertEqual(payload["metadata"]["resolution"], "1080p")
        self.assertTrue(payload["metadata"]["generate_audio"])

    def test_native_seedance_content_request_is_normalized(self):
        content = [
            {"type": "image_url", "image_url": {"url": "https://x/input.png"}},
            {"type": "text", "text": "native Seedance prompt"},
        ]
        _original, normalized = service.normalize_video_request(
            json.dumps(
                {
                    "model": "doubao-seedance-2.0-cf-1080p",
                    "content": content,
                    "ratio": "16:9",
                    "resolution": "1080p",
                    "duration": 4,
                }
            ).encode("utf-8")
        )
        payload = json.loads(normalized)
        self.assertEqual(payload["prompt"], "native Seedance prompt")
        self.assertEqual(payload["seconds"], "4")
        self.assertEqual(payload["images"], ["https://x/input.png"])
        self.assertEqual(payload["metadata"]["content"], content)
        self.assertEqual(payload["metadata"]["ratio"], "16:9")

    def test_video_size_derives_ratio_and_resolution(self):
        _original, normalized = service.normalize_video_request(
            json.dumps(
                {
                    "model": "doubao-seedance-2.0-cf-1080p",
                    "prompt": "portrait video",
                    "size": "2160x3840",
                    "duration": "4",
                }
            ).encode("utf-8")
        )
        payload = json.loads(normalized)
        self.assertEqual(payload["metadata"]["ratio"], "9:16")
        self.assertEqual(payload["metadata"]["resolution"], "1080p")

    def test_all_video_status_aliases_map_to_canonical_fetch(self):
        for prefix in service.VIDEO_STATUS_PREFIXES:
            target = service.video_status_target(prefix + "/task_123")
            expected = (
                "/pg/videos/task_123"
                if prefix == "/pg/videos"
                else "/v1/videos/task_123"
            )
            self.assertEqual(target, expected, prefix)

    def test_gemini_chat_response_becomes_image_response(self):
        chat_body = json.dumps(
            {
                "choices": [
                    {
                        "message": {
                            "content": f"![image](data:image/png;base64,{PNG_B64})"
                        }
                    }
                ]
            }
        ).encode("utf-8")
        body, mode = service.transform_image_response(
            {
                "model": "gemini-3.1-flash-image-preview",
                "response_format": "b64_json",
            },
            "application/json",
            chat_body,
        )
        payload = json.loads(body)
        self.assertEqual(mode, "gemini-b64_json")
        self.assertEqual(payload["data"][0]["b64_json"], PNG_B64)

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

            base64_endpoint = (
                f"http://127.0.0.1:{compat.server_port}/v1/images/generations"
            )
            base64_request = urllib.request.Request(
                base64_endpoint,
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

            gemini_request = urllib.request.Request(
                base64_endpoint,
                data=json.dumps(
                    {
                        "model": "gemini-3-pro-image-preview",
                        "prompt": "test",
                        "size": "4K",
                        "response_format": "b64_json",
                    }
                ).encode("utf-8"),
                headers={"Content-Type": "application/json"},
                method="POST",
            )
            with urllib.request.urlopen(gemini_request) as response:
                gemini_payload = json.load(response)
                self.assertEqual(
                    response.headers["X-Image-URL-Compat"], "gemini-b64_json"
                )
            self.assertEqual(MockUpstreamHandler.last_path, "/v1/chat/completions")
            self.assertEqual(
                MockUpstreamHandler.last_request["extra_body"]["google"][
                    "image_config"
                ]["image_size"],
                "4K",
            )
            self.assertEqual(gemini_payload["data"][0]["b64_json"], PNG_B64)

            edit_body, edit_content_type = multipart_body(
                {
                    "model": "gemini-3-pro-image-preview",
                    "prompt": "edit test",
                    "response_format": "url",
                },
                [("image", "input.png", "image/png", PNG_BYTES)],
            )
            edit_request = urllib.request.Request(
                f"http://127.0.0.1:{compat.server_port}/v1/images/edits",
                data=edit_body,
                headers={"Content-Type": edit_content_type},
                method="POST",
            )
            with urllib.request.urlopen(edit_request) as response:
                edit_payload = json.load(response)
                self.assertEqual(
                    response.headers["X-Image-URL-Compat"], "gemini-url"
                )
            self.assertEqual(MockUpstreamHandler.last_path, "/v1/chat/completions")
            self.assertIsInstance(
                MockUpstreamHandler.last_request["messages"][0]["content"], list
            )
            self.assertIn("url", edit_payload["data"][0])
            self.assertNotIn("b64_json", edit_payload["data"][0])

            for alias in service.VIDEO_CREATE_PATHS:
                video_request = urllib.request.Request(
                    f"http://127.0.0.1:{compat.server_port}{alias}",
                    data=json.dumps(
                        {
                            "model": "doubao-seedance-2.0-cf-1080p",
                            "prompt": "video test",
                            "aspect_ratio": "16:9",
                            "duration": 4,
                        }
                    ).encode("utf-8"),
                    headers={"Content-Type": "application/json"},
                    method="POST",
                )
                with urllib.request.urlopen(video_request) as response:
                    video_payload = json.load(response)
                    self.assertEqual(
                        response.headers["X-Mad-Compat"],
                        "video-create-normalized",
                    )
                expected_path = "/pg/videos" if alias == "/pg/videos" else "/v1/video/generations"
                self.assertEqual(MockUpstreamHandler.last_path, expected_path, alias)
                self.assertEqual(video_payload["id"], "task_seedance")
                self.assertEqual(MockUpstreamHandler.last_request["duration"], 4)
                self.assertEqual(
                    MockUpstreamHandler.last_request["metadata"]["ratio"], "16:9"
                )

            status_request = urllib.request.Request(
                f"http://127.0.0.1:{compat.server_port}/v1/contents/generations/tasks/task_seedance",
                method="GET",
            )
            with urllib.request.urlopen(status_request) as response:
                status_payload = json.load(response)
                self.assertEqual(
                    response.headers["X-Mad-Compat"],
                    "video-status-normalized",
                )
            self.assertEqual(MockUpstreamHandler.last_path, "/v1/videos/task_seedance")
            self.assertEqual(status_payload["status"], "completed")
            expected_video_url = (
                "https://mad.example/v1/videos/task_seedance/content"
            )
            self.assertEqual(status_payload["task_id"], "task_seedance")
            self.assertEqual(status_payload["url"], expected_video_url)
            self.assertEqual(status_payload["video_url"], expected_video_url)
            self.assertEqual(
                status_payload["metadata"]["url"], expected_video_url
            )
            self.assertEqual(
                status_payload["content"]["video_url"], expected_video_url
            )
            self.assertEqual(
                status_payload["data"]["video_url"], expected_video_url
            )
            self.assertEqual(
                status_payload["result"]["video_url"], expected_video_url
            )

            historical_body = json.dumps(
                {
                    "completed_at": 1784809243,
                    "created_at": 1784808893,
                    "id": "task_public",
                    "metadata": {
                        "url": "https://supplier.example/v1/videos/task_provider/content"
                    },
                    "model": "doubao-seedance-2.0-cf-1080p",
                    "object": "video",
                    "progress": 100,
                    "status": "completed",
                    "task_id": "task_provider",
                }
            ).encode("utf-8")
            normalized_body, normalized_mode = (
                service.normalize_video_status_response(
                    "/v1/contents/generations/tasks/task_public",
                    "application/json",
                    historical_body,
                )
            )
            normalized_payload = json.loads(normalized_body)
            historical_url = (
                "https://mad.example/v1/videos/task_public/content"
            )
            self.assertEqual(normalized_mode, "video-status-normalized")
            self.assertEqual(normalized_payload["task_id"], "task_public")
            self.assertEqual(
                normalized_payload["provider_task_id"], "task_provider"
            )
            self.assertEqual(normalized_payload["url"], historical_url)
            self.assertEqual(
                normalized_payload["content"]["video_url"], historical_url
            )

            for prefix in service.VIDEO_STATUS_PREFIXES:
                alias_body, alias_mode = service.normalize_video_status_response(
                    prefix + "/task_public",
                    "application/json",
                    historical_body,
                )
                alias_payload = json.loads(alias_body)
                self.assertEqual(
                    alias_mode, "video-status-normalized", prefix
                )
                self.assertEqual(alias_payload["id"], "task_public", prefix)
                self.assertEqual(alias_payload["url"], historical_url, prefix)
                self.assertEqual(
                    alias_payload["content"]["video_url"],
                    historical_url,
                    prefix,
                )

            stop_fallback_request = urllib.request.Request(
                f"http://127.0.0.1:{compat.server_port}/videos/generations",
                data=json.dumps(
                    {"model": "force-404", "prompt": "do not retry"}
                ).encode("utf-8"),
                headers={"Content-Type": "application/json"},
                method="POST",
            )
            with self.assertRaises(urllib.error.HTTPError) as raised:
                urllib.request.urlopen(stop_fallback_request)
            self.assertEqual(raised.exception.code, 502)

            playground_base64_request = urllib.request.Request(
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
            with urllib.request.urlopen(playground_base64_request) as response:
                playground_payload = json.load(response)
                self.assertEqual(response.headers["X-Image-URL-Compat"], "url")
            self.assertIn("url", playground_payload["data"][0])
            self.assertNotIn("b64_json", playground_payload["data"][0])

            options = urllib.request.Request(endpoint, method="OPTIONS")
            with urllib.request.urlopen(options) as response:
                self.assertEqual(response.status, 204)
                self.assertEqual(response.headers["Access-Control-Allow-Origin"], "*")
            self.assertEqual(MockUpstreamHandler.last_path, "/pg/images/generations")

            video_options = urllib.request.Request(
                f"http://127.0.0.1:{compat.server_port}/v1/contents/generations/tasks",
                method="OPTIONS",
            )
            with urllib.request.urlopen(video_options) as response:
                self.assertEqual(response.status, 204)
            self.assertEqual(MockUpstreamHandler.last_path, "/v1/video/generations")
        finally:
            compat.shutdown()
            compat.server_close()
            upstream.shutdown()
            upstream.server_close()
            service.UPSTREAM = previous_upstream


if __name__ == "__main__":
    unittest.main()
