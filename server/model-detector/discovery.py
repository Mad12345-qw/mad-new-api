import ipaddress
import asyncio
import socket
from dataclasses import asdict, dataclass
from typing import Any
from urllib.parse import urljoin, urlparse

import httpx


BLOCKED_MODEL_MARKERS = (
    "embedding",
    "image",
    "video",
    "audio",
    "speech",
    "tts",
    "realtime",
    "moderation",
    "whisper",
)


@dataclass
class ModelRoute:
    model: str
    family: str
    provider: str
    protocol: str
    endpoint: str
    fallbacks: list[str]
    supported_endpoint_types: list[str]
    owned_by: str
    discovered_via: str
    route_reason: str

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


def normalized_base_url(value: str) -> str:
    return value.rstrip("/")


def api_endpoint(base_url: str, path: str) -> str:
    parsed = urlparse(base_url)
    base_path = parsed.path.rstrip("/")
    clean_path = path if path.startswith("/") else f"/{path}"
    if base_path.endswith("/v1") and clean_path.startswith("/v1beta/"):
        parent_path = base_path[: -len("/v1")]
        rebuilt = parsed._replace(path=(parent_path + clean_path), params="", query="", fragment="")
        return rebuilt.geturl()
    if base_path.endswith("/v1") and clean_path.startswith("/v1/"):
        clean_path = clean_path[3:]
    return urljoin(normalized_base_url(base_url) + "/", clean_path.lstrip("/"))


def validate_public_api_url(value: str) -> None:
    parsed = urlparse(value)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname:
        raise ValueError("API address must be an absolute HTTP or HTTPS URL")
    try:
        addresses = {item[4][0] for item in socket.getaddrinfo(parsed.hostname, parsed.port or 443)}
    except socket.gaierror as exc:
        raise ValueError("API hostname cannot be resolved") from exc
    for address in addresses:
        ip = ipaddress.ip_address(address)
        if ip.is_private or ip.is_loopback or ip.is_link_local or ip.is_multicast or ip.is_reserved:
            raise ValueError("private, loopback, link-local, multicast, and reserved API addresses are not allowed")


def model_family(model: str, owned_by: str = "") -> str | None:
    value = f"{model} {owned_by}".lower()
    if "claude" in value:
        return "anthropic"
    if "gemini" in value:
        return "google"
    if model.lower().startswith(("gpt-", "o1", "o3", "o4")) or "openai" in owned_by.lower() or "codex" in value:
        return "openai"
    return None


def is_mainstream_text_model(model: str, family: str | None) -> bool:
    if family not in {"openai", "anthropic", "google"}:
        return False
    lower = model.lower()
    return not any(marker in lower for marker in BLOCKED_MODEL_MARKERS)


def route_for_model(
    model: str,
    owned_by: str = "",
    supported_endpoint_types: list[str] | None = None,
    discovered_via: str = "openai_models",
) -> ModelRoute | None:
    family = model_family(model, owned_by)
    if not is_mainstream_text_model(model, family):
        return None
    supported = sorted({str(item).strip().lower() for item in (supported_endpoint_types or []) if str(item).strip()})
    supported_text = " ".join(supported)
    if family == "anthropic":
        if "anthropic" in supported_text or "message" in supported_text:
            protocol, endpoint_path, fallbacks = "anthropic_messages", "/v1/messages", ["openai_chat"]
            reason = "模型列表声明 Anthropic Messages 能力"
        elif "openai" in supported_text:
            protocol, endpoint_path, fallbacks = "openai_chat", "/v1/chat/completions", []
            reason = "中转只声明 OpenAI 兼容端点，Claude 请求需经过协议转换层"
        else:
            protocol, endpoint_path, fallbacks = "anthropic_messages", "/v1/messages", ["openai_chat"]
            reason = "按 Claude 模型家族自动选择 Anthropic Messages，并保留明确未提交时的 OpenAI 回退"
        provider = "Anthropic"
    elif family == "google":
        if "gemini" in supported_text or "google" in supported_text or "generate" in supported_text:
            protocol, endpoint_path, fallbacks = "gemini_generate", f"/v1beta/models/{model}:generateContent", ["openai_chat"]
            reason = "模型列表声明 Gemini 原生端点能力"
        elif "openai" in supported_text:
            protocol, endpoint_path, fallbacks = "openai_chat", "/v1/chat/completions", []
            reason = "中转只声明 OpenAI 兼容端点，Gemini 请求需经过协议转换层"
        else:
            protocol, endpoint_path, fallbacks = "gemini_generate", f"/v1beta/models/{model}:generateContent", ["openai_chat"]
            reason = "按 Gemini 模型家族自动选择 generateContent"
        provider = "Google"
    else:
        if model.lower().startswith(("gpt-5", "o3", "o4")) or "codex" in model.lower():
            protocol, endpoint_path, fallbacks = "openai_responses", "/v1/responses", ["openai_chat"]
            reason = "OpenAI 最新推理/编码模型优先使用 Responses API"
        else:
            protocol, endpoint_path, fallbacks = "openai_chat", "/v1/chat/completions", ["openai_responses"]
            reason = "OpenAI 文本模型使用 Chat Completions"
        provider = "OpenAI"
    return ModelRoute(
        model=model,
        family=family or "unknown",
        provider=provider,
        protocol=protocol,
        endpoint=endpoint_path,
        fallbacks=fallbacks,
        supported_endpoint_types=supported,
        owned_by=owned_by,
        discovered_via=discovered_via,
        route_reason=reason,
    )


def parse_model_inventory(payload: Any, discovered_via: str) -> list[ModelRoute]:
    records: list[dict[str, Any]] = []
    if isinstance(payload, dict) and isinstance(payload.get("data"), list):
        records.extend(item for item in payload["data"] if isinstance(item, dict))
    if isinstance(payload, dict) and isinstance(payload.get("models"), list):
        for item in payload["models"]:
            if isinstance(item, dict):
                records.append(
                    {
                        "id": str(item.get("name", "")).removeprefix("models/"),
                        "owned_by": "google",
                        "supported_endpoint_types": ["gemini"],
                    }
                )
    result: list[ModelRoute] = []
    for record in records:
        model = str(record.get("id") or record.get("name") or "").removeprefix("models/").strip()
        if not model:
            continue
        supported = record.get("supported_endpoint_types")
        if not isinstance(supported, list) or not supported:
            supported = {
                "openai_models": ["openai"],
                "anthropic_models": ["anthropic"],
                "gemini_models": ["gemini"],
            }.get(discovered_via, [])
        route = route_for_model(
            model,
            str(record.get("owned_by") or ""),
            supported,
            discovered_via,
        )
        if route:
            result.append(route)
    return result


async def discover_models(base_url: str, api_key: str, timeout_seconds: float = 20.0) -> dict[str, Any]:
    attempts = [
        ("openai_models", "/v1/models", {"authorization": f"Bearer {api_key}"}),
        ("anthropic_models", "/v1/models", {"x-api-key": api_key, "anthropic-version": "2023-06-01"}),
        ("gemini_models", "/v1beta/models", {"x-goog-api-key": api_key}),
    ]
    discovered: dict[str, ModelRoute] = {}
    observations: list[dict[str, Any]] = []
    timeout = httpx.Timeout(timeout_seconds, connect=min(10.0, timeout_seconds))
    async with httpx.AsyncClient(timeout=timeout, follow_redirects=False, verify=True) as client:
        async def run_attempt(source: str, path: str, headers: dict[str, str]) -> tuple[list[ModelRoute], dict[str, Any]]:
            try:
                response = await client.get(api_endpoint(base_url, path), headers=headers)
                payload = response.json() if "json" in response.headers.get("content-type", "") else None
                routes = parse_model_inventory(payload, source) if response.status_code == 200 else []
                return routes, {"source": source, "path": path, "status_code": response.status_code, "models_found": len(routes)}
            except (httpx.HTTPError, ValueError) as exc:
                return [], {"source": source, "path": path, "status_code": None, "models_found": 0, "error": type(exc).__name__}

        attempt_results = await asyncio.gather(*(run_attempt(*attempt) for attempt in attempts))
        for routes, observation in attempt_results:
            observations.append(observation)
            for route in routes:
                current = discovered.get(route.model)
                if current is None or len(route.supported_endpoint_types) > len(current.supported_endpoint_types):
                    discovered[route.model] = route
    family_order = {"openai": 0, "anthropic": 1, "google": 2}
    models = sorted(discovered.values(), key=lambda item: (family_order.get(item.family, 9), item.model.lower()))
    return {"models": [item.to_dict() for item in models], "attempts": observations}
