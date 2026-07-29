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
    content = value.get("content")
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
