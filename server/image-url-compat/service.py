#!/usr/bin/env python3
import base64
import binascii
import ctypes
import gc
import hashlib
import hmac
import json
import logging
import math
import os
import secrets
import signal
import re
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from contextlib import contextmanager
from email import policy
from email.parser import BytesParser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


UPSTREAM = os.getenv("UPSTREAM", "http://127.0.0.1:3001")
PUBLIC_BASE_URL = os.getenv("PUBLIC_BASE_URL", "https://mad.myddns.me")
CACHE_DIR = Path(os.getenv("CACHE_DIR", "/opt/image-url-cache"))
CACHE_TTL = int(os.getenv("CACHE_TTL", "1800"))
CLEAN_INTERVAL = int(os.getenv("CLEAN_INTERVAL", "300"))
MAX_BODY = int(os.getenv("MAX_BODY", str(64 * 1024 * 1024)))
TIMEOUT = int(os.getenv("UPSTREAM_TIMEOUT", "650"))
IMAGE_DOWNLOAD_TIMEOUT = int(os.getenv("IMAGE_DOWNLOAD_TIMEOUT", "180"))
MAX_IMAGE_BYTES = int(os.getenv("MAX_IMAGE_BYTES", str(64 * 1024 * 1024)))
GEMINI_IMAGE_CONCURRENCY = int(os.getenv("GEMINI_IMAGE_CONCURRENCY", "2"))
SIGNED_VIDEO_URL_TTL = int(os.getenv("SIGNED_VIDEO_URL_TTL", "600"))
GEMINI_IMAGE_SLOTS = threading.BoundedSemaphore(max(1, GEMINI_IMAGE_CONCURRENCY))
try:
    LIBC = ctypes.CDLL("libc.so.6")
except OSError:
    LIBC = None

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
    "/images/generations",
    "/v1/images/generations",
    "/pg/images/generations",
}
IMAGE_EDIT_PATHS = {
    "/edits",
    "/v1/edits",
    "/images/edits",
    "/v1/images/edits",
    "/pg/images/edits",
    "/images/variations",
    "/v1/images/variations",
}
IMAGE_PATHS = IMAGE_GENERATION_PATHS | IMAGE_EDIT_PATHS
VIDEO_CREATE_PATHS = {
    "/pg/videos",
    "/videos/generations",
    "/v1/videos/generations",
    "/video/generations",
    "/v1/video/generations",
    "/contents/generations/tasks",
    "/v1/contents/generations/tasks",
    "/volc/v1/contents/generations/tasks",
    "/api/v3/contents/generations/tasks",
    "/v3/contents/generations/tasks",
    "/ark/api/v3/contents/generations/tasks",
}
VIDEO_STATUS_PREFIXES = VIDEO_CREATE_PATHS | {"/tasks", "/v1/tasks"}
VIDEO_METADATA_FIELDS = {
    "callback_url",
    "return_last_frame",
    "service_tier",
    "execution_expires_after",
    "generate_audio",
    "draft",
    "tools",
    "safety_identifier",
    "priority",
    "resolution",
    "ratio",
    "frames",
    "seed",
    "camera_fixed",
    "watermark",
    "audio",
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


def canonical_image_path(path):
    route, separator, query = path.partition("?")
    if route.startswith("/pg/"):
        target = (
            "/pg/images/generations"
            if route in IMAGE_GENERATION_PATHS
            else "/pg/images/edits"
        )
    else:
        target = (
            "/v1/images/generations"
            if route in IMAGE_GENERATION_PATHS
            else "/v1/images/edits"
        )
    return target + (separator + query if separator else "")


@contextmanager
def image_request_slot(request_json):
    if not is_gemini_image_model(request_json.get("model")):
        yield
        return
    acquired = GEMINI_IMAGE_SLOTS.acquire(timeout=TIMEOUT)
    if not acquired:
        raise TimeoutError("Gemini image compatibility queue timed out")
    try:
        yield
    finally:
        GEMINI_IMAGE_SLOTS.release()


def release_process_memory():
    gc.collect()
    if LIBC is not None:
        try:
            LIBC.malloc_trim(0)
        except Exception:
            pass


def json_image_references(request_json):
    references = []
    for key in ("image", "images", "input_image", "input_images"):
        value = request_json.get(key)
        values = value if isinstance(value, list) else [value]
        for item in values:
            url = media_url_value(item)
            if url:
                references.append(("image", url))
    content = request_json.get("content")
    if isinstance(content, list):
        for item in content:
            if not isinstance(item, dict) or item.get("type") != "image_url":
                continue
            url = media_url_value(item.get("image_url"))
            if url:
                references.append(("image", url))
    return list(dict.fromkeys(references))


def build_gemini_chat_body(request_json, image_parts=None):
    image_config = {"image_size": gemini_image_size(request_json)}
    aspect_ratio = str(request_json.get("aspect_ratio") or "").strip()
    if aspect_ratio:
        image_config["aspect_ratio"] = aspect_ratio
    content = str(request_json.get("prompt") or "Generate an image")
    references = []
    if image_parts:
        for field_name, mime_type, raw in image_parts:
            references.append(
                (
                    field_name,
                    f"data:{mime_type};base64," + base64.b64encode(raw).decode("ascii"),
                )
            )
    else:
        references = json_image_references(request_json)
    if references:
        content = [{"type": "text", "text": content}]
        for field_name, image_url in references:
            if field_name == "mask":
                content.append(
                    {
                        "type": "text",
                        "text": "Use the following image as the edit mask.",
                    }
                )
            content.append(
                {
                    "type": "image_url",
                    "image_url": {"url": image_url},
                }
            )
    payload = {
        "model": request_json.get("model"),
        "messages": [
            {
                "role": "user",
                "content": content,
            }
        ],
        "stream": False,
        "extra_body": {"google": {"image_config": image_config}},
    }
    group = request_json.get("group")
    if isinstance(group, str) and group.strip():
        payload["group"] = group
    return json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def parse_multipart_image_request(content_type, body):
    try:
        header = (
            "Content-Type: "
            + content_type
            + "\r\nMIME-Version: 1.0\r\n\r\n"
        ).encode("ascii")
    except UnicodeEncodeError as exc:
        raise ValueError("invalid multipart content type") from exc
    message = BytesParser(policy=policy.default).parsebytes(header + body)
    if not message.is_multipart():
        raise ValueError("invalid multipart image request")

    fields = {}
    image_parts = []
    for part in message.iter_parts():
        field_name = part.get_param("name", header="content-disposition")
        if not field_name:
            continue
        raw = part.get_payload(decode=True) or b""
        filename = part.get_filename()
        if filename is not None or field_name in {"image", "image[]", "mask"}:
            mime_type = part.get_content_type().lower()
            if not mime_type.startswith("image/"):
                raise ValueError("uploaded edit input is not an image")
            if not raw:
                raise ValueError("uploaded edit image is empty")
            if len(raw) > MAX_IMAGE_BYTES:
                raise ValueError("uploaded edit image is too large")
            normalized_name = "mask" if field_name == "mask" else "image"
            image_parts.append((normalized_name, mime_type, raw))
            continue

        charset = part.get_content_charset() or "utf-8"
        try:
            fields[field_name] = raw.decode(charset)
        except (LookupError, UnicodeDecodeError) as exc:
            raise ValueError("invalid multipart text field") from exc

    if not image_parts:
        raise ValueError("image edit request does not contain an image")
    return fields, image_parts


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


def prepare_upstream_request(path, body, content_type="application/json"):
    route = path.split("?", 1)[0]
    if route in IMAGE_EDIT_PATHS and "multipart/form-data" in content_type.lower():
        request_json, image_parts = parse_multipart_image_request(content_type, body)
        if is_gemini_image_model(request_json.get("model")):
            return (
                request_json,
                gemini_chat_path(path),
                build_gemini_chat_body(request_json, image_parts),
                "application/json",
            )
        return request_json, canonical_image_path(path), body, content_type

    request_json, upstream_body = prepare_upstream_body(body)
    if isinstance(request_json, dict) and is_gemini_image_model(
        request_json.get("model")
    ):
        return (
            request_json,
            gemini_chat_path(path),
            build_gemini_chat_body(request_json),
            "application/json",
        )
    return request_json, canonical_image_path(path), upstream_body, content_type


def media_url_value(value):
    if isinstance(value, str):
        return value.strip()
    if isinstance(value, dict):
        url = value.get("url")
        if isinstance(url, str):
            return url.strip()
    return ""


def normalize_video_request(body):
    try:
        payload = json.loads(body)
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        raise ValueError("invalid video request JSON") from exc
    if not isinstance(payload, dict):
        raise ValueError("video request must be a JSON object")

    prompt = str(payload.get("prompt") or "").strip()
    images = []
    single_image = payload.get("image")
    if isinstance(single_image, list):
        images.extend(url for item in single_image if (url := media_url_value(item)))
    elif url := media_url_value(single_image):
        images.append(url)
    raw_images = payload.get("images")
    if isinstance(raw_images, list):
        images.extend(url for item in raw_images if (url := media_url_value(item)))
    for key in ("image_url", "input_reference"):
        if url := media_url_value(payload.get(key)):
            images.append(url)
    reference_images = payload.get("reference_image_urls")
    if isinstance(reference_images, list):
        images.extend(
            url for item in reference_images if (url := media_url_value(item))
        )

    content = payload.get("content")
    if isinstance(content, list):
        text_parts = []
        for item in content:
            if not isinstance(item, dict):
                continue
            if item.get("type") == "text" and isinstance(item.get("text"), str):
                text = item["text"].strip()
                if text:
                    text_parts.append(text)
            if item.get("type") == "image_url":
                url = media_url_value(item.get("image_url"))
                if url:
                    images.append(url)
        if not prompt and text_parts:
            prompt = "\n".join(text_parts)

    normalized = {
        "model": payload.get("model"),
        "prompt": prompt,
    }
    for key in ("group", "duration", "seconds", "size", "mode"):
        if key in payload:
            normalized[key] = payload[key]
    if "duration" in payload and "seconds" not in normalized:
        normalized["seconds"] = str(payload["duration"])
    if images:
        normalized["images"] = list(dict.fromkeys(images))

    metadata = payload.get("metadata")
    if not isinstance(metadata, dict):
        metadata = {}
    else:
        metadata = dict(metadata)
    if isinstance(content, list):
        metadata["content"] = content
    for key in VIDEO_METADATA_FIELDS:
        if key in payload:
            metadata[key] = payload[key]
    aspect_ratio = payload.get("aspect_ratio")
    if isinstance(aspect_ratio, str) and aspect_ratio.strip():
        metadata.setdefault("ratio", aspect_ratio.strip())
    size = payload.get("size")
    if isinstance(size, str):
        match = re.fullmatch(r"(\d{2,5})[Xx](\d{2,5})", size.strip())
        if match:
            width, height = int(match.group(1)), int(match.group(2))
            if width and height:
                divisor = math.gcd(width, height)
                ratio = f"{width // divisor}:{height // divisor}"
                if ratio in {"1:1", "4:3", "3:4", "16:9", "9:16", "21:9"}:
                    metadata.setdefault("ratio", ratio)
                metadata.setdefault(
                    "resolution", "1080p" if max(width, height) >= 1920 else "720p"
                )
    if "audio" in payload:
        metadata.setdefault("generate_audio", payload["audio"])
    if metadata:
        normalized["metadata"] = metadata

    return payload, json.dumps(
        normalized, ensure_ascii=False, separators=(",", ":")
    ).encode("utf-8")


def video_status_target(path):
    route, separator, query = path.partition("?")
    for prefix in sorted(VIDEO_STATUS_PREFIXES, key=len, reverse=True):
        marker = prefix + "/"
        if not route.startswith(marker):
            continue
        task_id = route[len(marker) :]
        if task_id and "/" not in task_id:
            target = (
                "/pg/videos/" + task_id
                if prefix == "/pg/videos"
                else "/v1/videos/" + task_id
            )
            return target + (separator + query if separator else "")
    return None


def video_task_id_from_path(path):
    route = path.split("?", 1)[0]
    for prefix in sorted(VIDEO_STATUS_PREFIXES, key=len, reverse=True):
        marker = prefix + "/"
        if not route.startswith(marker):
            continue
        task_id = route[len(marker) :]
        if task_id and "/" not in task_id:
            return task_id
    return ""


def public_video_content_url(task_id):
    if not task_id:
        return ""
    return (
        PUBLIC_BASE_URL.rstrip("/")
        + "/v1/videos/"
        + urllib.parse.quote(task_id, safe="")
        + "/content"
    )


def authorization_token_key(authorization):
    value = str(authorization or "").strip()
    if value.lower().startswith("bearer "):
        value = value[7:].strip()
    if value.startswith("sk-"):
        value = value[3:]
    return value.split("-", 1)[0].strip()


def signed_video_content_url(task_id, authorization, now=None):
    content_url = public_video_content_url(task_id)
    token_key = authorization_token_key(authorization)
    if not content_url or not token_key:
        return content_url
    expires = int(time.time() if now is None else now) + SIGNED_VIDEO_URL_TTL
    expires_text = str(expires)
    payload = (task_id + "\n" + expires_text).encode("utf-8")
    signature = hmac.new(
        token_key.encode("utf-8"), payload, hashlib.sha256
    ).hexdigest()
    return content_url + "?" + urllib.parse.urlencode(
        {"expires": expires_text, "signature": signature}
    )


def normalize_video_create_response(content_type, response_body):
    if "application/json" not in content_type.lower():
        return response_body, "video-create"
    try:
        payload = json.loads(response_body)
    except (json.JSONDecodeError, UnicodeDecodeError):
        return response_body, "video-create"
    if not isinstance(payload, dict):
        return response_body, "video-create"
    public_task_id = str(payload.get("id") or payload.get("task_id") or "").strip()
    if not public_task_id:
        return response_body, "video-create"
    provider_task_id = str(payload.get("task_id") or "").strip()
    if provider_task_id and provider_task_id != public_task_id:
        payload.setdefault("provider_task_id", provider_task_id)
    payload["id"] = public_task_id
    payload["task_id"] = public_task_id
    return json.dumps(
        payload, ensure_ascii=False, separators=(",", ":")
    ).encode("utf-8"), "video-create-normalized"


def normalize_video_status_response(
    path, content_type, response_body, authorization=""
):
    if "application/json" not in content_type.lower():
        return response_body, "video-status"
    try:
        payload = json.loads(response_body)
    except (json.JSONDecodeError, UnicodeDecodeError):
        return response_body, "video-status"
    if not isinstance(payload, dict):
        return response_body, "video-status"

    path_task_id = video_task_id_from_path(path)
    payload_task_id = str(payload.get("id") or "").strip()
    public_task_id = str(path_task_id or payload_task_id).strip()
    provider_task_id = str(payload.get("task_id") or "").strip()
    if not provider_task_id and payload_task_id != public_task_id:
        provider_task_id = payload_task_id
    if provider_task_id and provider_task_id != public_task_id:
        payload.setdefault("provider_task_id", provider_task_id)
    if public_task_id:
        payload["id"] = public_task_id
        payload["task_id"] = public_task_id

    status = str(payload.get("status") or "").strip().lower()
    if status not in {"completed", "succeeded", "success", "done"}:
        return json.dumps(
            payload, ensure_ascii=False, separators=(",", ":")
        ).encode("utf-8"), "video-status-normalized"

    content_url = signed_video_content_url(public_task_id, authorization)
    if not content_url:
        return response_body, "video-status"

    metadata = payload.get("metadata")
    metadata = dict(metadata) if isinstance(metadata, dict) else {}
    metadata["url"] = content_url
    metadata["video_url"] = content_url
    payload["metadata"] = metadata
    payload["url"] = content_url
    payload["video_url"] = content_url
    payload["content_url"] = content_url

    content = payload.get("content")
    content = dict(content) if isinstance(content, dict) else {}
    content["url"] = content_url
    content["video_url"] = content_url
    payload["content"] = content

    data = payload.get("data")
    data = dict(data) if isinstance(data, dict) else {}
    data["id"] = public_task_id
    data["task_id"] = public_task_id
    data["status"] = payload.get("status")
    data["url"] = content_url
    data["video_url"] = content_url
    payload["data"] = data

    result = payload.get("result")
    result = dict(result) if isinstance(result, dict) else {}
    result["url"] = content_url
    result["video_url"] = content_url
    payload["result"] = result

    return json.dumps(
        payload, ensure_ascii=False, separators=(",", ":")
    ).encode("utf-8"), "video-status-normalized"


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
    server_version = "ImageURLCompat/6.2"
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        LOG.info("%s %s", self.address_string(), fmt % args)

    def handle_one_request(self):
        try:
            super().handle_one_request()
        finally:
            release_process_memory()

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

    def upstream_headers(self, body_length=None, content_type=None):
        headers = {}
        for key, value in self.headers.items():
            if (
                key.lower() not in HOP_HEADERS
                and key.lower() != "host"
                and not (content_type and key.lower() == "content-type")
            ):
                headers[key] = value
        if body_length is not None:
            headers["Content-Length"] = str(body_length)
        if content_type:
            headers["Content-Type"] = content_type
        return headers

    def forward_response(self, status, response_headers, response_body, mode):
        self.send_response(status)
        for key, value in response_headers.items():
            if key.lower() not in HOP_HEADERS and key.lower() != "content-length":
                self.send_header(key, value)
        self.send_header("Content-Length", str(len(response_body)))
        self.send_header("X-Image-URL-Compat", mode)
        self.send_header("X-Mad-Compat", mode)
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
        status_target = video_status_target(self.path)
        if status_target:
            try:
                status, response_headers, response_body = self.request_upstream(
                    "GET", headers=self.upstream_headers(), path=status_target
                )
                client_status = 502 if status in {404, 405} else status
                mode = "video-status"
                if 200 <= status < 300:
                    response_body, mode = normalize_video_status_response(
                        self.path,
                        response_headers.get("Content-Type", ""),
                        response_body,
                        self.headers.get("Authorization", ""),
                    )
                self.forward_response(
                    client_status, response_headers, response_body, mode
                )
            except Exception:
                LOG.exception("upstream video status request failed")
                self.send_json_error(502, "upstream request failed")
            return
        self.send_json_error(404, "not found")

    def do_OPTIONS(self):
        route = self.path.split("?", 1)[0]
        target = self.path
        if route in VIDEO_CREATE_PATHS:
            target = "/pg/videos" if route == "/pg/videos" else "/v1/video/generations"
        elif route in IMAGE_PATHS:
            target = canonical_image_path(self.path)
        else:
            status_target = video_status_target(self.path)
            if status_target:
                target = status_target
        try:
            status, response_headers, response_body = self.request_upstream(
                "OPTIONS", headers=self.upstream_headers(), path=target
            )
            self.forward_response(status, response_headers, response_body, "passthrough")
        except Exception:
            LOG.exception("upstream OPTIONS request failed")
            self.send_json_error(502, "upstream request failed")

    def do_POST(self):
        route = self.path.split("?", 1)[0]
        if route not in IMAGE_PATHS and route not in VIDEO_CREATE_PATHS:
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
        if route in VIDEO_CREATE_PATHS:
            try:
                _request_json, upstream_body = normalize_video_request(body)
            except ValueError as exc:
                self.send_json_error(400, str(exc))
                return
            try:
                status, response_headers, response_body = self.request_upstream(
                    "POST",
                    body=upstream_body,
                    headers=self.upstream_headers(
                        len(upstream_body), content_type="application/json"
                    ),
                    path=(
                        "/pg/videos"
                        if route == "/pg/videos"
                        else "/v1/video/generations"
                    ),
                )
                client_status = 502 if status in {404, 405} else status
                mode = "video-create"
                if 200 <= status < 300:
                    response_body, mode = normalize_video_create_response(
                        response_headers.get("Content-Type", ""), response_body
                    )
                self.forward_response(
                    client_status, response_headers, response_body, mode
                )
            except Exception:
                LOG.exception("upstream video create request failed")
                self.send_json_error(502, "upstream request failed")
            return

        try:
            request_json, upstream_path, upstream_body, upstream_content_type = (
                prepare_upstream_request(
                    self.path,
                    body,
                    self.headers.get("Content-Type", "application/octet-stream"),
                )
            )
        except ValueError as exc:
            self.send_json_error(400, str(exc))
            return
        try:
            with image_request_slot(request_json):
                status, response_headers, response_body = self.request_upstream(
                    "POST",
                    body=upstream_body,
                    headers=self.upstream_headers(
                        len(upstream_body), content_type=upstream_content_type
                    ),
                    path=upstream_path,
                )
                mode = "passthrough"
                content_type = response_headers.get("Content-Type", "")
                if 200 <= status < 300:
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
        except TimeoutError as exc:
            self.send_json_error(503, str(exc))
            return
        except Exception:
            LOG.exception("upstream image request or normalization failed")
            self.send_json_error(502, "upstream image request failed")
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
