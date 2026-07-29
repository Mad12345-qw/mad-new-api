import asyncio
import hashlib
import json
import os
import re
import time
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any
from urllib.parse import urljoin, urlparse

import httpx

from discovery import ModelRoute, api_endpoint, route_for_model
from security import decrypt_secret


def load_rule_pack() -> dict[str, Any]:
    default_path = Path(__file__).resolve().parent / "rules" / "default.json"
    path = Path(os.environ.get("DETECTOR_RULE_FILE", str(default_path)))
    with path.open("r", encoding="utf-8") as handle:
        return json.load(handle)


RULE_PACK = load_rule_pack()
RULE_VERSION = str(RULE_PACK["version"])
MAX_CAPTURE_BYTES = 256_000
SENSITIVE_HEADERS = {
    "authorization",
    "cookie",
    "set-cookie",
    "x-api-key",
    "api-key",
    "x-goog-api-key",
    "proxy-authorization",
}


@dataclass
class Evidence:
    probe: str
    category: str
    strength: str
    supports: str | None
    title: str
    detail: dict[str, Any]
    raw_sha256: str | None = None


@dataclass
class ProbeResponse:
    status_code: int
    elapsed_ms: int
    headers: dict[str, str]
    body_text: str
    body_json: Any
    raw_sha256: str


def sanitize_headers(headers: httpx.Headers | dict[str, str]) -> dict[str, str]:
    cleaned: dict[str, str] = {}
    for key, value in headers.items():
        lower = key.lower()
        if lower in SENSITIVE_HEADERS:
            continue
        if any(token in lower for token in ("token", "secret", "key")):
            continue
        cleaned[lower] = value[:500]
    return cleaned


def safe_json(text: str) -> Any:
    try:
        return json.loads(text)
    except (json.JSONDecodeError, TypeError):
        return None


def normalize_base_url(value: str) -> str:
    return value.rstrip("/") + "/"


def endpoint(base_url: str, path: str) -> str:
    parsed = urlparse(base_url)
    base_path = parsed.path.rstrip("/")
    clean_path = path if path.startswith("/") else f"/{path}"
    if base_path.endswith("/v1") and clean_path.startswith("/v1/"):
        clean_path = clean_path[3:]
    return urljoin(normalize_base_url(base_url), clean_path.lstrip("/"))


def auth_headers(style: str, api_key: str) -> dict[str, str]:
    if style == "anthropic":
        return {
            "x-api-key": api_key,
            "anthropic-version": "2023-06-01",
            "content-type": "application/json",
        }
    if style == "gemini":
        return {"x-goog-api-key": api_key, "content-type": "application/json"}
    return {"authorization": f"Bearer {api_key}", "content-type": "application/json"}


def protocol_headers(protocol: str, api_key: str) -> dict[str, str]:
    if protocol == "anthropic_messages":
        return auth_headers("anthropic", api_key)
    if protocol == "gemini_generate":
        return auth_headers("gemini", api_key)
    return auth_headers("openai", api_key)


async def captured_request(
    client: httpx.AsyncClient,
    method: str,
    url: str,
    headers: dict[str, str],
    payload: dict[str, Any] | None = None,
) -> ProbeResponse:
    started = time.perf_counter()
    response = await client.request(method, url, headers=headers, json=payload)
    raw = response.content[:MAX_CAPTURE_BYTES]
    elapsed = int((time.perf_counter() - started) * 1000)
    text = raw.decode("utf-8", errors="replace")
    return ProbeResponse(
        status_code=response.status_code,
        elapsed_ms=elapsed,
        headers=sanitize_headers(response.headers),
        body_text=text,
        body_json=safe_json(text),
        raw_sha256=hashlib.sha256(response.content).hexdigest(),
    )


def transport_evidence(base_url: str) -> list[Evidence]:
    host = (urlparse(base_url).hostname or "").lower()
    for rule in RULE_PACK.get("domain_rules", []):
        if re.search(str(rule["pattern"]), host):
            return [
                Evidence(
                    probe="endpoint",
                    category="endpoint",
                    strength="strong",
                    supports=str(rule["channel"]),
                    title=str(rule["title"]),
                    detail={"hostname": host},
                )
            ]
    return [
        Evidence(
            probe="endpoint",
            category="endpoint",
            strength="weak",
            supports="relay_or_custom",
            title="自定义或中转域名",
            detail={"hostname": host, "note": "域名本身不能证明隐藏的真实上游"},
        )
    ]


def header_evidence(probe: str, response: ProbeResponse) -> list[Evidence]:
    found: list[Evidence] = []
    names = set(response.headers)
    for rule in RULE_PACK.get("header_rules", []):
        channel = str(rule["channel"])
        required = tuple(str(item).lower() for item in rule["all_headers"])
        strength = str(rule["strength"])
        title = str(rule["title"])
        if all(name in names for name in required):
            found.append(
                Evidence(
                    probe=probe,
                    category="headers",
                    strength=strength,
                    supports=channel,
                    title=title,
                    detail={"matched_headers": list(required)},
                    raw_sha256=response.raw_sha256,
                )
            )
    found.append(
        Evidence(
            probe=probe,
            category="observation",
            strength="info",
            supports=None,
            title="脱敏响应摘要",
            detail={
                "status_code": response.status_code,
                "elapsed_ms": response.elapsed_ms,
                "headers": response.headers,
                "body_shape": body_shape(response.body_json),
            },
            raw_sha256=response.raw_sha256,
        )
    )
    return found


def body_shape(value: Any, depth: int = 0) -> Any:
    if depth >= 3:
        return type(value).__name__
    if isinstance(value, dict):
        return {str(key): body_shape(item, depth + 1) for key, item in list(value.items())[:30]}
    if isinstance(value, list):
        return [body_shape(item, depth + 1) for item in value[:3]]
    if value is None:
        return "null"
    return type(value).__name__


def nested_keys(value: Any, depth: int = 0) -> set[str]:
    if depth > 6:
        return set()
    if isinstance(value, dict):
        keys = {str(key) for key in value}
        for item in value.values():
            keys.update(nested_keys(item, depth + 1))
        return keys
    if isinstance(value, list):
        keys: set[str] = set()
        for item in value[:20]:
            keys.update(nested_keys(item, depth + 1))
        return keys
    return set()


def payload_evidence(probe: str, response: ProbeResponse, configured_models: list[str] | None = None) -> list[Evidence]:
    value = response.body_json
    if not isinstance(value, dict):
        return []
    result: list[Evidence] = []
    for rule in RULE_PACK.get("error_rules", []):
        if re.search(str(rule["pattern"]), response.body_text, flags=re.IGNORECASE):
            result.append(
                Evidence(
                    probe,
                    "errors",
                    str(rule["strength"]),
                    str(rule["channel"]),
                    str(rule["title"]),
                    {"matched_rule": str(rule["pattern"])},
                    response.raw_sha256,
                )
            )
    if probe == "models":
        inventory: list[str] = []
        data = value.get("data")
        if isinstance(data, list):
            inventory.extend(str(item.get("id")) for item in data if isinstance(item, dict) and item.get("id"))
        models = value.get("models")
        if isinstance(models, list):
            inventory.extend(str(item.get("name")) for item in models if isinstance(item, dict) and item.get("name"))
        inventory = list(dict.fromkeys(inventory))[:1000]
        comparable = set(inventory) | {item.removeprefix("models/") for item in inventory}
        missing = [model for model in (configured_models or []) if model not in comparable]
        result.append(
            Evidence(
                probe,
                "model_list",
                "info",
                None,
                "模型列表快照",
                {"count": len(inventory), "models": inventory, "configured_models_missing": missing},
                response.raw_sha256,
            )
        )
    identifier = str(value.get("id", ""))
    if identifier.startswith("chatcmpl-") or identifier.startswith("resp_"):
        result.append(Evidence(probe, "payload", "medium", "openai_protocol", "OpenAI 响应对象标识", {"id_prefix": identifier.split("-")[0][:12]}))
    if identifier.startswith("msg_") and value.get("type") == "message":
        result.append(Evidence(probe, "payload", "medium", "anthropic_protocol", "Anthropic Messages 对象结构", {"type": value.get("type")}))
    if "candidates" in value and "usageMetadata" in value:
        result.append(Evidence(probe, "payload", "medium", "google_generative_protocol", "Google Generative 响应结构", {"modelVersion": value.get("modelVersion")}))
    error = value.get("error")
    if isinstance(error, dict):
        detail = {
            "error_keys": sorted(error.keys()),
            "type": error.get("type"),
            "code": error.get("code") or error.get("status"),
        }
        result.append(Evidence(probe, "errors", "info", None, "错误对象契约", detail, response.raw_sha256))
    usage = value.get("usage") or value.get("usageMetadata")
    if isinstance(usage, dict):
        result.append(
            Evidence(
                probe,
                "token_accounting",
                "info",
                None,
                "Token 与缓存字段",
                {"usage_keys": sorted(str(key) for key in usage.keys()), "usage": usage},
                response.raw_sha256,
            )
        )
    keys = nested_keys(value)
    matched_tool_keys = sorted(keys & {"tool_calls", "tool_use", "functionCall", "function_call", "functionCalls"})
    content = value.get("content")
    content_block_types = (
        {str(block.get("type")) for block in content if isinstance(block, dict) and block.get("type")}
        if isinstance(content, list)
        else set()
    )
    matched_tool_types = sorted(content_block_types & {"tool_use"})
    if matched_tool_keys or matched_tool_types:
        result.append(
            Evidence(
                probe,
                "tool_structure",
                "medium",
                None,
                "工具调用结构",
                {"matched_keys": matched_tool_keys, "matched_block_types": matched_tool_types, "shape": body_shape(value)},
                response.raw_sha256,
            )
        )
    if isinstance(content, list):
        block_types = [str(block.get("type")) for block in content if isinstance(block, dict) and block.get("type")]
        if block_types:
            result.append(
                Evidence(
                    probe,
                    "thinking_structure",
                    "weak",
                    None,
                    "思考与内容块结构",
                    {"block_types": block_types, "note": "签名和块类型可被网关改写，不能单独证明模型身份"},
                    response.raw_sha256,
                )
            )
    response_model = value.get("model") or value.get("modelVersion")
    if response_model:
        result.append(
            Evidence(
                probe,
                "response_model",
                "weak",
                None,
                "响应声明模型",
                {"response_model": str(response_model), "note": "该字段可被中转站改写，不能单独作为真伪证据"},
                response.raw_sha256,
            )
        )
    return result


def model_alias_evidence(models: list[str]) -> list[Evidence]:
    result: list[Evidence] = []
    for model in models:
        normalized = model.lower()
        if any(marker in normalized for marker in ("fable-5", "opus-5", "gpt-5.6-sol")):
            result.append(
                Evidence(
                    probe="configured_models",
                    category="model_alias",
                    strength="weak",
                    supports=None,
                    title=f"最新模型别名需要可信参考：{model}",
                    detail={
                        "model": model,
                        "note": "模型名可被任意网关改写；必须结合可信参考上游和至少两类独立强证据",
                    },
                )
            )
    return result


def model_list_path(style: str) -> str:
    return "/v1beta/models" if style == "gemini" else "/v1/models"


def invalid_probe(style: str) -> tuple[str, dict[str, Any]]:
    invalid_model = "__model_detector_invalid__"
    if style == "anthropic":
        return "/v1/messages", {"model": invalid_model, "max_tokens": 1, "messages": [{"role": "user", "content": "X"}]}
    if style == "gemini":
        return f"/v1beta/models/{invalid_model}:generateContent", {"contents": [{"role": "user", "parts": [{"text": "X"}]}], "generationConfig": {"maxOutputTokens": 1}}
    return "/v1/chat/completions", {"model": invalid_model, "max_tokens": 1, "messages": [{"role": "user", "content": "X"}]}


def missing_model_probe(style: str) -> tuple[str, dict[str, Any]]:
    if style == "anthropic":
        return "/v1/messages", {"max_tokens": 1, "messages": [{"role": "user", "content": "X"}]}
    if style == "gemini":
        return "/v1beta/models/:generateContent", {"contents": [{"role": "user", "parts": [{"text": "X"}]}]}
    return "/v1/chat/completions", {"max_tokens": 1, "messages": [{"role": "user", "content": "X"}]}


def active_probe(style: str, model: str, stream: bool = False) -> tuple[str, dict[str, Any]]:
    if style == "anthropic":
        payload: dict[str, Any] = {
            "model": model,
            "max_tokens": 1,
            "stream": stream,
            "messages": [{"role": "user", "content": "Reply with X only."}],
        }
        if any(marker in model.lower() for marker in ("fable-5", "opus-5")):
            payload["effort"] = "low"
            payload["thinking"] = {"type": "adaptive", "display": "omitted"}
        return "/v1/messages", payload
    if style == "gemini":
        suffix = "streamGenerateContent?alt=sse" if stream else "generateContent"
        return f"/v1beta/models/{model}:{suffix}", {
            "contents": [{"role": "user", "parts": [{"text": "Reply with X only."}]}],
            "generationConfig": {"maxOutputTokens": 1, "temperature": 0},
        }
    if model.lower().startswith("gpt-5"):
        return "/v1/responses", {
            "model": model,
            "input": "Reply with X only.",
            "max_output_tokens": 16,
            "stream": stream,
            "reasoning": {"effort": "low"},
        }
    return "/v1/chat/completions", {
        "model": model,
        "max_tokens": 1,
        "stream": stream,
        "temperature": 0,
        "messages": [{"role": "user", "content": "Reply with X only."}],
    }


def route_probe(route: ModelRoute, stream: bool = False) -> tuple[str, dict[str, Any]]:
    model = route.model
    if route.protocol == "anthropic_messages":
        payload: dict[str, Any] = {
            "model": model,
            "max_tokens": 1,
            "stream": stream,
            "messages": [{"role": "user", "content": "Reply with X only."}],
        }
        if any(marker in model.lower() for marker in ("fable-5", "opus-5")):
            payload["effort"] = "low"
            payload["thinking"] = {"type": "adaptive", "display": "omitted"}
        return "/v1/messages", payload
    if route.protocol == "gemini_generate":
        suffix = "streamGenerateContent?alt=sse" if stream else "generateContent"
        return f"/v1beta/models/{model}:{suffix}", {
            "contents": [{"role": "user", "parts": [{"text": "Reply with X only."}]}],
            "generationConfig": {"maxOutputTokens": 1, "temperature": 0},
        }
    if route.protocol == "openai_responses":
        return "/v1/responses", {
            "model": model,
            "input": "Reply with X only.",
            "max_output_tokens": 16,
            "stream": stream,
            "reasoning": {"effort": "low"},
        }
    return "/v1/chat/completions", {
        "model": model,
        "max_tokens": 1,
        "stream": stream,
        "temperature": 0,
        "messages": [{"role": "user", "content": "Reply with X only."}],
        **(
            {"effort": "low", "thinking": {"type": "adaptive", "display": "omitted"}}
            if route.family == "anthropic" and any(marker in model.lower() for marker in ("fable-5", "opus-5"))
            else {}
        ),
    }


def capability_probe(route: ModelRoute) -> tuple[str, dict[str, Any]]:
    tool_name = "detector_marker"
    parameters = {
        "type": "object",
        "properties": {"marker": {"type": "string", "enum": ["X"]}},
        "required": ["marker"],
        "additionalProperties": False,
    }
    if route.protocol == "anthropic_messages":
        return "/v1/messages", {
            "model": route.model,
            "max_tokens": 32,
            "messages": [{"role": "user", "content": "Call detector_marker with marker X."}],
            "tools": [{"name": tool_name, "description": "Return the fixed marker", "input_schema": parameters}],
            "tool_choice": {"type": "tool", "name": tool_name},
        }
    if route.protocol == "gemini_generate":
        return f"/v1beta/models/{route.model}:generateContent", {
            "contents": [{"role": "user", "parts": [{"text": "Call detector_marker with marker X."}]}],
            "tools": [{"functionDeclarations": [{"name": tool_name, "description": "Return the fixed marker", "parameters": parameters}]}],
            "toolConfig": {"functionCallingConfig": {"mode": "ANY", "allowedFunctionNames": [tool_name]}},
            "generationConfig": {"maxOutputTokens": 32, "temperature": 0},
        }
    if route.protocol == "openai_responses":
        return "/v1/responses", {
            "model": route.model,
            "input": "Call detector_marker with marker X.",
            "max_output_tokens": 32,
            "tools": [{"type": "function", "name": tool_name, "description": "Return the fixed marker", "parameters": parameters, "strict": True}],
            "tool_choice": {"type": "function", "name": tool_name},
        }
    return "/v1/chat/completions", {
        "model": route.model,
        "max_tokens": 32,
        "messages": [{"role": "user", "content": "Call detector_marker with marker X."}],
        "tools": [{"type": "function", "function": {"name": tool_name, "description": "Return the fixed marker", "parameters": parameters, "strict": True}}],
        "tool_choice": {"type": "function", "function": {"name": tool_name}},
    }


def protocol_translation(route: ModelRoute) -> bool:
    return (route.family == "anthropic" and route.protocol.startswith("openai")) or (
        route.family == "google" and route.protocol.startswith("openai")
    )


def route_with_protocol(route: ModelRoute, protocol: str) -> ModelRoute:
    endpoint_path = {
        "anthropic_messages": "/v1/messages",
        "gemini_generate": f"/v1beta/models/{route.model}:generateContent",
        "openai_responses": "/v1/responses",
        "openai_chat": "/v1/chat/completions",
    }[protocol]
    data = route.to_dict()
    data["protocol"] = protocol
    data["endpoint"] = endpoint_path
    data["fallbacks"] = [item for item in route.fallbacks if item != protocol]
    data["route_reason"] = f"首选端点明确返回未找到/不支持，自动回退到 {protocol}"
    return ModelRoute(**data)


def observed_chain(evidence: list[Evidence], route: ModelRoute, terminal: dict[str, Any]) -> dict[str, Any]:
    layers: list[dict[str, Any]] = []
    header_names: set[str] = set()
    error_types: set[str] = set()
    for item in evidence:
        if item.category == "observation":
            headers = item.detail.get("headers")
            if isinstance(headers, dict):
                header_names.update(str(key).lower() for key in headers)
        if item.category == "errors" and item.detail.get("type"):
            error_types.add(str(item.detail["type"]))
    if "x-oneapi-request-id" in header_names or "new_api_error" in error_types:
        layers.append(
            {
                "position": "outer",
                "kind": "new_api_gateway",
                "label": "New API / One API 外层网关",
                "confidence": 0.98,
                "status": "confirmed",
            }
        )
    elif any(name in header_names for name in ("apim-request-id", "x-ms-region")):
        layers.append(
            {"position": "outer", "kind": "azure_apim", "label": "Azure APIM 外层", "confidence": 0.9, "status": "confirmed"}
        )
    else:
        layers.append(
            {"position": "outer", "kind": "custom_relay", "label": "自定义中转入口", "confidence": 0.6, "status": "observed"}
        )
    intermediary_markers = [
        (any(name.startswith("x-litellm") for name in header_names), "litellm_marker", "LiteLLM 风格中间层标记"),
        (any(name.startswith("x-openrouter") for name in header_names), "openrouter_marker", "OpenRouter 风格中间层标记"),
        ("via" in header_names or "x-envoy-upstream-service-time" in header_names, "proxy_marker", "额外 HTTP/Envoy 代理标记"),
        ("cf-ray" in header_names, "edge_proxy", "Cloudflare 边缘代理"),
    ]
    for matched, kind, label in intermediary_markers:
        if matched:
            layers.append(
                {
                    "position": "intermediate",
                    "kind": kind,
                    "label": label,
                    "confidence": 0.7,
                    "status": "observed",
                    "note": "该标记可能由外层保留或仿造，因此不单独增加已确认跳数",
                }
            )
    if protocol_translation(route):
        layers.append(
            {
                "position": "translation",
                "kind": "protocol_translation",
                "label": f"{route.provider} 模型经 {route.protocol} 协议转换",
                "confidence": 0.95,
                "status": "confirmed",
                "note": "转换可能发生在外层网关本身，不单独证明增加了一跳",
            }
        )
    terminal_channel = terminal.get("likely_channel", "unknown")
    if terminal_channel != "unknown" and terminal.get("verdict") != "inconclusive":
        layers.append(
            {
                "position": "terminal",
                "kind": terminal_channel,
                "label": terminal_channel,
                "confidence": terminal.get("confidence", 0.0),
                "status": "probable",
            }
        )
    else:
        layers.append(
            {
                "position": "terminal",
                "kind": "unknown_terminal",
                "label": "终端上游被中转层清洗，当前不可见",
                "confidence": 0.0,
                "status": "unknown",
            }
        )
    # A protocol conversion or a preserved proxy header can be implemented by the
    # outer gateway itself.  They are observable layers, but are not proof of an
    # additional physical relay hop.  Keep the network-hop lower bound separate
    # from the number of visible logical layers to avoid overstating a multi-hop
    # chain.
    confirmed_hops = 1 + (1 if layers[-1]["kind"] != "unknown_terminal" else 0)
    return {
        "layers": layers,
        "observed_logical_layers": len(layers),
        "minimum_confirmed_hops": confirmed_hops,
        "unknown_intermediate_possible": True,
        "note": "逻辑层不等于物理跳数；完全清洗指纹的中间层无法从黑盒响应中枚举，跳数仅是可证明的下限",
    }


def sse_evidence(response: ProbeResponse) -> list[Evidence]:
    content_type = response.headers.get("content-type", "")
    if "text/event-stream" not in content_type and not response.body_text.startswith("data:"):
        return []
    events: list[str] = []
    data_shapes: list[Any] = []
    for line in response.body_text.splitlines():
        if line.startswith("event:"):
            events.append(line.partition(":")[2].strip())
        elif line.startswith("data:"):
            payload = line.partition(":")[2].strip()
            if payload != "[DONE]":
                data_shapes.append(body_shape(safe_json(payload)))
    supports = None
    strength = "info"
    if any(event.startswith("message_") or event.startswith("content_block_") for event in events):
        supports, strength = "anthropic_protocol", "medium"
    elif any(event.startswith("response.") for event in events):
        supports, strength = "openai_protocol", "medium"
    elif any(isinstance(shape, dict) and "choices" in shape for shape in data_shapes):
        supports, strength = "openai_protocol", "medium"
    return [Evidence("active_stream", "sse", strength, supports, "SSE 事件序列", {"events": events[:50], "data_shapes": data_shapes[:10]}, response.raw_sha256)]


def classify(evidence: list[Evidence], claimed_channel: str = "unknown") -> dict[str, Any]:
    channel_categories: dict[str, set[str]] = {}
    channel_score: dict[str, float] = {}
    weights = {"strong": 0.46, "medium": 0.24, "weak": 0.08, "info": 0.0}
    for item in evidence:
        if not item.supports or item.supports in {"relay_or_custom", "openai_protocol", "anthropic_protocol", "google_generative_protocol"}:
            continue
        channel_categories.setdefault(item.supports, set()).add(item.category)
        channel_score[item.supports] = channel_score.get(item.supports, 0.0) + weights.get(item.strength, 0.0)
    if not channel_score:
        return {"verdict": "inconclusive", "likely_channel": "unknown", "confidence": 0.0, "summary": "证据不足，无法判断真实上游渠道"}
    likely = max(channel_score, key=channel_score.get)
    categories = channel_categories[likely]
    confidence = min(0.99, channel_score[likely])
    endpoint_support = any(item.category == "endpoint" and item.supports == likely and item.strength == "strong" for item in evidence)
    direct_channels = {"openai_official", "anthropic_official", "gemini_developer_api"}
    alternate_channels = {"azure_openai", "aws_bedrock", "vertex_ai"}
    if likely in direct_channels and endpoint_support and len(categories) >= 2:
        verdict = "confirmed_direct"
        summary = "官方域名与独立协议指纹一致"
    elif likely in alternate_channels and len(categories) >= 2 and confidence >= 0.65:
        if claimed_channel in direct_channels:
            verdict = "suspected_substitution"
            summary = "供应商宣称官方直连，但至少两类独立证据指向其他云渠道"
        else:
            verdict = "probable_alternate_channel"
            summary = "至少两类独立证据指向其他云渠道"
    else:
        verdict = "inconclusive"
        summary = "存在渠道线索，但尚未达到自动判定门槛"
    return {"verdict": verdict, "likely_channel": likely, "confidence": round(confidence, 2), "summary": summary}


class DetectorEngine:
    def __init__(self, timeout_seconds: float = 30.0) -> None:
        self.timeout_seconds = timeout_seconds

    async def run(self, upstream: dict[str, Any], mode: str) -> tuple[dict[str, Any], list[Evidence]]:
        style = upstream["api_style"]
        key = decrypt_secret(upstream["api_key_encrypted"])
        models = json.loads(upstream.get("models_json") or "[]")
        base_url = upstream["base_url"]
        headers = auth_headers(style, key)
        evidence = transport_evidence(base_url) + model_alias_evidence(models)
        completed_safe_probes = 0
        planned_safe_probes = 6
        limits = httpx.Limits(max_connections=4, max_keepalive_connections=2)
        timeout = httpx.Timeout(self.timeout_seconds, connect=min(10.0, self.timeout_seconds))
        async with httpx.AsyncClient(timeout=timeout, limits=limits, follow_redirects=False, verify=True) as client:
            if await self._probe(client, "models", "GET", endpoint(base_url, model_list_path(style)), headers, None, evidence, models):
                completed_safe_probes += 1
            invalid_path, invalid_payload = invalid_probe(style)
            if await self._probe(client, "invalid_model", "POST", endpoint(base_url, invalid_path), headers, invalid_payload, evidence, models):
                completed_safe_probes += 1
            invalid_headers = auth_headers(style, "model-detector-invalid-key")
            if await self._probe(client, "invalid_auth", "GET", endpoint(base_url, model_list_path(style)), invalid_headers, None, evidence, models):
                completed_safe_probes += 1
            missing_path, missing_payload = missing_model_probe(style)
            if await self._probe(client, "missing_model", "POST", endpoint(base_url, missing_path), headers, missing_payload, evidence, models):
                completed_safe_probes += 1
            if await self._probe(client, "models_head", "HEAD", endpoint(base_url, model_list_path(style)), headers, None, evidence, models):
                completed_safe_probes += 1
            if await self._probe(client, "unknown_route", "GET", endpoint(base_url, "/v1/model-detector-probe-not-found"), headers, None, evidence, models):
                completed_safe_probes += 1
            if mode == "active" and upstream.get("allow_paid_probes"):
                for model in models[:8]:
                    active_path, active_payload = active_probe(style, model, False)
                    await self._probe(client, f"active_sync:{model}", "POST", endpoint(base_url, active_path), headers, active_payload, evidence, models)
                    stream_path, stream_payload = active_probe(style, model, True)
                    response = await self._probe(client, f"active_stream:{model}", "POST", endpoint(base_url, stream_path), headers, stream_payload, evidence, models)
                    if response:
                        evidence.extend(sse_evidence(response))
            elif mode == "active":
                evidence.append(Evidence("active", "safety", "info", None, "主动探针已阻止", {"reason": "该上游未显式允许可能计费的探针"}))
        coverage = completed_safe_probes / planned_safe_probes
        evidence.append(
            Evidence(
                "coverage",
                "quality_gate",
                "info",
                None,
                "探针覆盖率",
                {"completed": completed_safe_probes, "planned": planned_safe_probes, "ratio": round(coverage, 3), "required_ratio": 0.8},
            )
        )
        result = classify(evidence, upstream.get("claimed_channel", "unknown"))
        if coverage < 0.8:
            result = {
                "verdict": "inconclusive",
                "likely_channel": result["likely_channel"],
                "confidence": 0.0,
                "summary": "成功探针不足 80%，按失败关闭原则无法判断",
            }
        return result, evidence

    async def run_models(self, upstream: dict[str, Any], routes: list[dict[str, Any]]) -> list[dict[str, Any]]:
        if not upstream.get("allow_paid_probes"):
            return [
                {
                    "model": str(route.get("model", "")),
                    "family": str(route.get("family", "unknown")),
                    "protocol": str(route.get("protocol", "unknown")),
                    "verdict": "inconclusive",
                    "likely_channel": "unknown",
                    "confidence": 0.0,
                    "summary": "该上游未显式允许低 Token 主动探针",
                    "chain": {"layers": [], "minimum_confirmed_hops": 0, "unknown_intermediate_possible": True},
                    "evidence": [],
                }
                for route in routes
            ]
        api_key = decrypt_secret(upstream["api_key_encrypted"])
        base_url = upstream["base_url"]
        results: list[dict[str, Any]] = []
        timeout = httpx.Timeout(self.timeout_seconds, connect=min(10.0, self.timeout_seconds))
        limits = httpx.Limits(max_connections=4, max_keepalive_connections=2)
        async with httpx.AsyncClient(timeout=timeout, limits=limits, follow_redirects=False, verify=True) as client:
            for route_data in routes[:30]:
                route = ModelRoute(**route_data)
                model_evidence: list[Evidence] = [
                    Evidence(
                        "route",
                        "route_selection",
                        "info",
                        None,
                        "自动协议匹配",
                        {
                            "model": route.model,
                            "family": route.family,
                            "protocol": route.protocol,
                            "endpoint": route.endpoint,
                            "supported_endpoint_types": route.supported_endpoint_types,
                            "reason": route.route_reason,
                        },
                    )
                ]
                model_evidence.extend(transport_evidence(base_url))
                responses: list[ProbeResponse] = []
                for stream in (False, True):
                    path, payload = route_probe(route, stream)
                    probe_name = "model_stream" if stream else "model_sync"
                    try:
                        response = await captured_request(
                            client,
                            "POST",
                            api_endpoint(base_url, path),
                            protocol_headers(route.protocol, api_key),
                            payload,
                        )
                    except (httpx.HTTPError, asyncio.TimeoutError) as exc:
                        model_evidence.append(
                            Evidence(
                                probe_name,
                                "transport_error",
                                "info",
                                None,
                                "模型穿透探针失败",
                                {"error_type": type(exc).__name__, "message": str(exc)[:500]},
                            )
                        )
                        continue
                    responses.append(response)
                    model_evidence.extend(header_evidence(probe_name, response))
                    model_evidence.extend(payload_evidence(probe_name, response, [route.model]))
                    if not stream and response.status_code in {404, 405} and route.fallbacks:
                        fallback = route.fallbacks[0]
                        model_evidence.append(
                            Evidence(
                                "route_fallback",
                                "route_selection",
                                "info",
                                None,
                                "自动协议回退",
                                {"from": route.protocol, "to": fallback, "reason_status": response.status_code},
                                response.raw_sha256,
                            )
                        )
                        route = route_with_protocol(route, fallback)
                        path, payload = route_probe(route, False)
                        try:
                            response = await captured_request(
                                client,
                                "POST",
                                api_endpoint(base_url, path),
                                protocol_headers(route.protocol, api_key),
                                payload,
                            )
                            responses.append(response)
                            model_evidence.extend(header_evidence("model_sync_fallback", response))
                            model_evidence.extend(payload_evidence("model_sync_fallback", response, [route.model]))
                        except (httpx.HTTPError, asyncio.TimeoutError) as exc:
                            model_evidence.append(
                                Evidence(
                                    "model_sync_fallback",
                                    "transport_error",
                                    "info",
                                    None,
                                    "回退协议探针失败",
                                    {"error_type": type(exc).__name__, "message": str(exc)[:500]},
                                )
                            )
                    if stream:
                        model_evidence.extend(sse_evidence(response))
                capability_path, capability_payload = capability_probe(route)
                try:
                    capability_response = await captured_request(
                        client,
                        "POST",
                        api_endpoint(base_url, capability_path),
                        protocol_headers(route.protocol, api_key),
                        capability_payload,
                    )
                    responses.append(capability_response)
                    model_evidence.extend(header_evidence("model_capability", capability_response))
                    model_evidence.extend(payload_evidence("model_capability", capability_response, [route.model]))
                except (httpx.HTTPError, asyncio.TimeoutError) as exc:
                    model_evidence.append(
                        Evidence(
                            "model_capability",
                            "transport_error",
                            "info",
                            None,
                            "工具能力探针失败",
                            {"error_type": type(exc).__name__, "message": str(exc)[:500]},
                        )
                    )
                expected = {
                    "openai": "openai_official",
                    "anthropic": "anthropic_official",
                    "google": "gemini_developer_api",
                }.get(route.family, "unknown")
                terminal = classify(model_evidence, expected)
                success_count = sum(1 for response in responses if 200 <= response.status_code < 300)
                if success_count == 0:
                    terminal = {
                        "verdict": "inconclusive",
                        "likely_channel": terminal.get("likely_channel", "unknown"),
                        "confidence": 0.0,
                        "summary": "有效模型请求未成功穿透，无法判断终端上游",
                    }
                elif terminal["verdict"] == "inconclusive" and protocol_translation(route):
                    terminal = {
                        "verdict": "inconclusive",
                        "likely_channel": "unknown",
                        "confidence": 0.0,
                        "summary": f"已确认 {route.provider} 模型通过 {route.protocol} 非原生协议转换；该事实不等于终端渠道证据，终端上游仍未知",
                    }
                chain = observed_chain(model_evidence, route, terminal)
                results.append(
                    {
                        "model": route.model,
                        "family": route.family,
                        "protocol": route.protocol,
                        "endpoint": route.endpoint,
                        "verdict": terminal["verdict"],
                        "likely_channel": terminal["likely_channel"],
                        "confidence": terminal["confidence"],
                        "summary": terminal["summary"],
                        "success_probes": success_count,
                        "planned_probes": 3,
                        "chain": chain,
                        "evidence": model_evidence,
                    }
                )
        return results

    async def _probe(
        self,
        client: httpx.AsyncClient,
        name: str,
        method: str,
        url: str,
        headers: dict[str, str],
        payload: dict[str, Any] | None,
        evidence: list[Evidence],
        configured_models: list[str],
    ) -> ProbeResponse | None:
        try:
            response = await captured_request(client, method, url, headers, payload)
        except (httpx.HTTPError, asyncio.TimeoutError) as exc:
            evidence.append(Evidence(name, "transport_error", "info", None, "探针请求失败", {"error_type": type(exc).__name__, "message": str(exc)[:500]}))
            return None
        evidence.extend(header_evidence(name, response))
        evidence.extend(payload_evidence(name, response, configured_models))
        return response


def evidence_to_row(item: Evidence) -> dict[str, Any]:
    value = asdict(item)
    value["detail_json"] = json.dumps(value.pop("detail"), ensure_ascii=False, separators=(",", ":"))
    return value
