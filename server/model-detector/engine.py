import asyncio
import hashlib
import json
import os
import re
import secrets
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
    timeout_seconds: float | None = None,
) -> ProbeResponse:
    started = time.perf_counter()
    request_options: dict[str, Any] = {}
    if timeout_seconds is not None:
        request_options["timeout"] = timeout_seconds
    response = await client.request(method, url, headers=headers, json=payload, **request_options)
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


def _usage_from_response(value: dict[str, Any]) -> dict[str, Any]:
    usage = value.get("usage")
    return usage if isinstance(usage, dict) else {}


def _claude_usage(usage: dict[str, Any]) -> dict[str, Any]:
    billing = usage.get("billing_usage")
    if isinstance(billing, dict) and isinstance(billing.get("claude_usage"), dict):
        return billing["claude_usage"]
    return usage


def _completed_sse_response(response: ProbeResponse) -> dict[str, Any] | None:
    """Return the final provider response embedded in a Responses API SSE stream."""
    completed: dict[str, Any] | None = None
    for line in response.body_text.splitlines():
        if not line.startswith("data:"):
            continue
        payload = line.partition(":")[2].strip()
        if not payload or payload == "[DONE]":
            continue
        event = safe_json(payload)
        if not isinstance(event, dict) or not isinstance(event.get("response"), dict):
            continue
        if event.get("type") == "response.completed":
            return event["response"]
        completed = event["response"]
    return completed


def _provider_response_value(response: ProbeResponse) -> dict[str, Any] | None:
    if isinstance(response.body_json, dict):
        return response.body_json
    return _completed_sse_response(response)


def _response_route_profile(
    probe: str,
    request_payload: dict[str, Any],
    response: ProbeResponse,
) -> dict[str, Any] | None:
    value = _provider_response_value(response)
    if not isinstance(value, dict) or not 200 <= response.status_code < 300:
        return None
    usage = _usage_from_response(value)
    native_usage = _claude_usage(usage)
    input_tokens = usage.get("prompt_tokens", usage.get("input_tokens", native_usage.get("input_tokens")))
    if not isinstance(input_tokens, int) or isinstance(input_tokens, bool):
        input_tokens = 0
    cache_creation = native_usage.get("cache_creation_input_tokens", usage.get("cache_creation_input_tokens", 0))
    if not isinstance(cache_creation, int) or isinstance(cache_creation, bool):
        cache_creation = 0
    billing = usage.get("billing_usage")
    billing = billing if isinstance(billing, dict) else {}
    instructions = value.get("instructions")
    visible_request_chars = len(json.dumps(request_payload, ensure_ascii=False, separators=(",", ":")))
    return {
        "probe": probe,
        "input_tokens": input_tokens,
        "cache_creation_input_tokens": cache_creation,
        "hidden_instruction_chars": len(instructions) if isinstance(instructions, str) else 0,
        "hidden_instruction_sha256": (
            hashlib.sha256(instructions.encode("utf-8")).hexdigest() if isinstance(instructions, str) and instructions else None
        ),
        "visible_request_chars": visible_request_chars,
        "usage_source": usage.get("usage_source"),
        "billing_source": billing.get("source"),
        "response_object": value.get("object") or value.get("type"),
    }


def within_run_route_divergence_evidence(route: ModelRoute, profiles: list[dict[str, Any]]) -> Evidence | None:
    lightweight = [item for item in profiles if int(item.get("input_tokens") or 0) <= 200]
    hidden = [
        item
        for item in profiles
        if int(item.get("input_tokens") or 0) >= 1000 or int(item.get("hidden_instruction_chars") or 0) >= 4000
    ]
    if not lightweight or not hidden:
        return None
    low = min(lightweight, key=lambda item: int(item.get("input_tokens") or 0))
    high = max(hidden, key=lambda item: int(item.get("input_tokens") or 0))
    ratio = round(int(high.get("input_tokens") or 0) / max(1, int(low.get("input_tokens") or 0)), 1)
    return Evidence(
        "within_run_consistency",
        "within_run_route_divergence",
        "strong",
        "heterogeneous_backend_pool",
        f"同一轮 {route.model} 固定小探针进入互斥后端路径",
        {
            "rule_id": "within_run_route_divergence_v1",
            "lightweight_profile": low,
            "hidden_or_amplified_profile": high,
            "input_token_ratio": ratio,
            "observed_profiles": profiles,
            "conclusion": "同一模型在同一轮检测中至少命中轻量路径与隐藏提示/放大路径，不能视为稳定、单一的官方直连渠道",
        },
    )


def cliproxyapi_header_fingerprint(headers: dict[str, str]) -> dict[str, Any] | None:
    rule = RULE_PACK.get("implementation_rules", {}).get("cliproxyapi", {})
    trace_headers = [str(item).lower() for item in rule.get("trace_headers", ["x-cpa-trace-id"])]
    direct = [name for name in trace_headers if name in headers]
    exposed = headers.get("access-control-expose-headers", "").lower()
    expose_markers = [str(item).lower() for item in rule.get("cors_expose_markers", [])]
    matched_exposed = [marker for marker in expose_markers if marker in exposed]
    if not direct and not matched_exposed:
        return None
    trace_format_valid = False
    trace_value = headers.get("x-cpa-trace-id", "")
    if trace_value:
        trace_format_valid = bool(re.fullmatch(r"\d{14}-[a-zA-Z0-9]+-[a-zA-Z0-9]+", trace_value))
    return {
        "direct_header_names": direct,
        "cors_expose_markers": matched_exposed,
        "trace_format_valid": trace_format_valid,
        "source_urls": [str(item) for item in rule.get("source_urls", [])],
    }


def implementation_evidence(probe: str, route: ModelRoute, response: ProbeResponse) -> list[Evidence]:
    fingerprint = cliproxyapi_header_fingerprint(response.headers)
    if not fingerprint:
        return []
    supports = None
    if route.family == "openai":
        supports = "codex_subscription_relay"
    elif route.family == "anthropic":
        supports = "claude_subscription_relay"
    elif route.family == "google":
        supports = "gemini_compatibility_relay"
    return [
        Evidence(
            probe,
            "cliproxyapi_implementation",
            "strong",
            supports,
            "检测到 CLIProxyAPI 专属 CPA 响应头",
            {
                "rule_id": "cliproxyapi_cpa_headers_v1",
                **fingerprint,
                "note": "X-CPA-TRACE-ID 及 CPA CORS 暴露列表由 CLIProxyAPI 源码直接定义；可证明该实现或其直接分支位于链路中，但不能单独枚举其前后的物理跳数",
            },
            response.raw_sha256,
        )
    ]


def provenance_evidence(
    probe: str,
    route: ModelRoute,
    request_payload: dict[str, Any],
    response: ProbeResponse,
) -> list[Evidence]:
    """Extract high-specificity subscription relay fingerprints.

    These rules intentionally require a combination of behavior, payload, and
    header evidence.  Model names and gateway-added compatibility fields never
    become terminal-channel proof by themselves.
    """
    value = response.body_json
    extracted_from_sse = False
    if not isinstance(value, dict):
        value = _completed_sse_response(response)
        extracted_from_sse = isinstance(value, dict)
    if not isinstance(value, dict) or not 200 <= response.status_code < 300:
        return []

    result: list[Evidence] = implementation_evidence(probe, route, response)
    rules = RULE_PACK.get("subscription_relay_rules", {})
    codex_rule = rules.get("codex", {})
    model_text = route.model.lower()
    if route.protocol == "openai_responses" and re.search(str(codex_rule.get("model_pattern", r"gpt|codex")), model_text):
        instructions = value.get("instructions")
        markers = [str(item) for item in codex_rule.get("instruction_markers", [])]
        matched = [marker for marker in markers if isinstance(instructions, str) and marker in instructions]
        minimum_chars = int(codex_rule.get("minimum_instruction_chars", 4000))
        minimum_matches = int(codex_rule.get("minimum_marker_matches", 3))
        if (
            "instructions" not in request_payload
            and isinstance(instructions, str)
            and len(instructions) >= minimum_chars
            and len(matched) >= minimum_matches
        ):
            result.append(
                Evidence(
                    probe,
                    "codex_prompt_fingerprint",
                    "strong",
                    str(codex_rule.get("channel", "codex_subscription_relay")),
                    "检测到未请求的 Codex 固定系统指令",
                    {
                        "rule_id": "codex_hidden_instructions_v1",
                        "instruction_chars": len(instructions),
                        "instruction_sha256": hashlib.sha256(instructions.encode("utf-8")).hexdigest(),
                        "matched_markers": matched,
                        "first_line": instructions.splitlines()[0][:240] if instructions else "",
                        "request_supplied_instructions": False,
                        "extracted_from_sse": extracted_from_sse,
                        "source_urls": [str(item) for item in codex_rule.get("source_urls", [])],
                    },
                    response.raw_sha256,
                )
            )

        generated_fields = [
            field
            for field in ("prompt_cache_key", "safety_identifier")
            if field not in request_payload and isinstance(value.get(field), str) and value.get(field)
        ]
        max_tokens_rewritten = (
            isinstance(request_payload.get("max_output_tokens"), int)
            and value.get("max_output_tokens") is None
        )
        if len(generated_fields) >= 2 and max_tokens_rewritten:
            result.append(
                Evidence(
                    probe,
                    "request_rewrite",
                    "medium",
                    str(codex_rule.get("channel", "codex_subscription_relay")),
                    "请求字段被 Codex 代理层自动补写或改写",
                    {
                        "rule_id": "codex_request_rewrite_v1",
                        "generated_fields": generated_fields,
                        "requested_max_output_tokens": request_payload.get("max_output_tokens"),
                        "response_max_output_tokens": value.get("max_output_tokens"),
                        "extracted_from_sse": extracted_from_sse,
                    },
                    response.raw_sha256,
                )
            )

        usage = _usage_from_response(value)
        input_tokens = usage.get("input_tokens")
        input_value = request_payload.get("input")
        visible_chars = len(input_value) if isinstance(input_value, str) else len(json.dumps(input_value, ensure_ascii=False))
        minimum_tokens = int(codex_rule.get("minimum_amplified_input_tokens", 1000))
        if "instructions" not in request_payload and visible_chars <= 500 and isinstance(input_tokens, int) and input_tokens >= minimum_tokens:
            official_baseline = (
                RULE_PACK.get("official_baselines", {})
                .get("openai_responses", {})
                .get("minimal_input_tokens", {})
                .get(route.model)
            )
            result.append(
                Evidence(
                    probe,
                    "token_amplification",
                    "medium",
                    str(codex_rule.get("channel", "codex_subscription_relay")),
                    "极短输入产生异常大量输入 Token",
                    {
                        "rule_id": "codex_token_amplification_v1",
                        "visible_input_chars": visible_chars,
                        "reported_input_tokens": input_tokens,
                        "minimum_rule_tokens": minimum_tokens,
                        "paired_official_input_tokens": official_baseline,
                        "amplification_vs_official": (
                            round(input_tokens / official_baseline, 1)
                            if isinstance(official_baseline, int) and official_baseline > 0
                            else None
                        ),
                        "official_baseline_captured_at": RULE_PACK.get("official_baselines", {})
                        .get("openai_responses", {})
                        .get("captured_at"),
                    },
                    response.raw_sha256,
                )
            )

        header_matches = {
            key: response.headers[key]
            for key in ("via", "x-client-request-id", "x-new-api-version")
            if key in response.headers
        }
        if "x-client-request-id" in header_matches and "via" in header_matches:
            result.append(
                Evidence(
                    probe,
                    "relay_headers",
                    "weak",
                    str(codex_rule.get("channel", "codex_subscription_relay")),
                    "Codex 代理链响应头组合",
                    {"rule_id": "codex_relay_headers_v1", "matched_headers": header_matches},
                    response.raw_sha256,
                )
            )

    claude_rule = rules.get("claude_code", {})
    if route.family == "anthropic" and re.search(str(claude_rule.get("model_pattern", "claude")), model_text):
        usage = _usage_from_response(value)
        native_usage = _claude_usage(usage)
        cache_creation = native_usage.get("cache_creation_input_tokens")
        total_input = usage.get("prompt_tokens", usage.get("input_tokens", native_usage.get("input_tokens")))
        minimum_cache = int(claude_rule.get("minimum_cache_creation_tokens", 2000))
        minimum_input = int(claude_rule.get("minimum_total_input_tokens", 2500))
        has_no_system = "system" not in request_payload
        native_input = native_usage.get("input_tokens")
        cache_read = native_usage.get("cache_read_input_tokens")
        if isinstance(native_input, int) and isinstance(cache_creation, int):
            native_total = native_input + cache_creation + (cache_read if isinstance(cache_read, int) else 0)
            total_input = max(total_input if isinstance(total_input, int) else 0, native_total)
        if (
            has_no_system
            and isinstance(cache_creation, int)
            and cache_creation >= minimum_cache
            and isinstance(total_input, int)
            and total_input >= minimum_input
        ):
            result.append(
                Evidence(
                    probe,
                    "claude_hidden_prompt_cache",
                    "strong",
                    str(claude_rule.get("channel", "claude_subscription_relay")),
                    "小型工具请求出现巨量 Claude 缓存创建 Token",
                    {
                        "rule_id": "claude_code_cache_injection_v1",
                        "reported_total_input_tokens": total_input,
                        "cache_creation_input_tokens": cache_creation,
                        "request_supplied_system": False,
                        "note": "高度符合 Claude Code/OAuth 系统提示注入；自定义网关仍可仿造，不能单独定案",
                        "source_urls": [str(item) for item in claude_rule.get("source_urls", [])],
                    },
                    response.raw_sha256,
                )
            )

        billing = usage.get("billing_usage")
        metadata_match = (
            usage.get("usage_source") == "anthropic"
            and isinstance(billing, dict)
            and billing.get("source") == "claude_messages"
        )
        if metadata_match:
            result.append(
                Evidence(
                    probe,
                    "gateway_translation_metadata",
                    "info",
                    None,
                    "New API Claude 转换计费字段",
                    {
                        "usage_source": usage.get("usage_source"),
                        "billing_source": billing.get("source"),
                        "note": "这是协议转换证据，不是 Anthropic 官方渠道签名",
                    },
                    response.raw_sha256,
                )
            )

        relay_headers = {
            key: response.headers[key]
            for key in ("x-client-request-id", "x-request-id", "x-new-api-version")
            if key in response.headers
        }
        if "x-client-request-id" in relay_headers and ("x-request-id" in relay_headers or "x-new-api-version" in relay_headers):
            result.append(
                Evidence(
                    probe,
                    "relay_headers",
                    "medium",
                    str(claude_rule.get("channel", "claude_subscription_relay")),
                    "Claude Code/OAuth 风格代理响应头组合",
                    {"rule_id": "claude_code_relay_headers_v1", "matched_headers": relay_headers},
                    response.raw_sha256,
                )
            )
        thinking_request = request_payload.get("thinking")
        content = value.get("content")
        if (
            isinstance(thinking_request, dict)
            and thinking_request.get("display") == "omitted"
            and isinstance(content, list)
        ):
            visible_thinking = [
                block
                for block in content
                if isinstance(block, dict)
                and block.get("type") == "thinking"
                and isinstance(block.get("thinking"), str)
                and block.get("thinking")
            ]
            if visible_thinking:
                result.append(
                    Evidence(
                        probe,
                        "request_contract_rewrite",
                        "strong",
                        None,
                        "请求 display=omitted 但返回了可读 thinking 内容",
                        {
                            "rule_id": "claude_omitted_thinking_rewrite_v1",
                            "visible_thinking_blocks": len(visible_thinking),
                            "visible_thinking_chars": sum(len(str(block.get("thinking"))) for block in visible_thinking),
                            "note": "证明中转层没有透明保留所提交的 Claude 请求/响应契约；不能单独识别最终云渠道",
                        },
                        response.raw_sha256,
                    )
                )
    antigravity_rule = rules.get("antigravity", {})
    if route.family == "google" and route.model.lower() == str(antigravity_rule.get("model", "")).lower():
        usage = _usage_from_response(value)
        requested_limit = request_payload.get("max_tokens", request_payload.get("max_output_tokens"))
        output_tokens = usage.get("completion_tokens", usage.get("output_tokens"))
        minimum_rewritten = int(antigravity_rule.get("minimum_rewritten_output_tokens", 4))
        if (
            isinstance(requested_limit, int)
            and requested_limit == int(antigravity_rule.get("maximum_requested_output_tokens", 1))
            and isinstance(output_tokens, int)
            and output_tokens >= minimum_rewritten
        ):
            result.append(
                Evidence(
                    probe,
                    "gemini_max_token_rewrite",
                    "strong",
                    "gemini_compatibility_relay",
                    "Gemini 兼容链忽略了请求的 1 Token 输出上限",
                    {
                        "rule_id": "gemini_openai_max_token_rewrite_v1",
                        "requested_output_tokens": requested_limit,
                        "reported_output_tokens": output_tokens,
                        "minimum_rule_tokens": minimum_rewritten,
                        "note": "CLIProxyAPI Antigravity 执行器会删除非 Claude 请求的 maxOutputTokens；其他兼容层也可能忽略限制，因此需与隐藏别名或 CPA 指纹组合判断",
                        "source_urls": [str(item) for item in antigravity_rule.get("source_urls", [])],
                    },
                    response.raw_sha256,
                )
            )
    return result


def model_alias_evidence(models: list[str]) -> list[Evidence]:
    result: list[Evidence] = []
    official_openai = {
        "gpt-5.5",
        "gpt-5.6-luna",
        "gpt-5.6-sol",
        "gpt-5.6-terra",
        "gpt-image-2",
    }
    official_anthropic = {"claude-fable-5", "claude-opus-4-8", "claude-opus-5"}
    official_google = {"gemini-3.6-flash"}
    for model in models:
        normalized = model.lower()
        if normalized in official_openai or normalized in official_anthropic or normalized in official_google:
            source_url = (
                f"https://developers.openai.com/api/docs/models/{normalized}"
                if normalized in official_openai
                else "https://platform.claude.com/docs/en/about-claude/models/overview"
                if normalized in official_anthropic
                else f"https://ai.google.dev/gemini-api/docs/models/{normalized}"
            )
            result.append(
                Evidence(
                    probe="configured_models",
                    category="model_alias",
                    strength="weak",
                    supports=None,
                    title=f"模型名称与官方目录一致：{model}",
                    detail={
                        "model": model,
                        "official_model_id": True,
                        "source_urls": [source_url],
                        "note": "只证明名称是真实存在的官方模型 ID；模型名可被网关改写，不能据此证明本次请求到达该模型或官方渠道",
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
        if any(marker in model.lower() for marker in ("fable-5", "opus-5", "opus-4-8")):
            payload["thinking"] = {"type": "adaptive", "display": "omitted"}
            payload["output_config"] = {"effort": "low"}
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
            "reasoning": {"effort": "none", "context": "current_turn"},
            "store": False,
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
        if any(marker in model.lower() for marker in ("fable-5", "opus-5", "opus-4-8")):
            payload["thinking"] = {"type": "adaptive", "display": "omitted"}
            payload["output_config"] = {"effort": "low"}
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
            "reasoning": {"effort": "none", "context": "current_turn"},
            "store": False,
        }
    return "/v1/chat/completions", {
        "model": model,
        "max_tokens": 1,
        "stream": stream,
        "temperature": 0,
        "messages": [
            {
                "role": "user",
                "content": (
                    "Output exactly these twelve space-separated tokens: A B C D E F G H I J K L"
                    if route.family == "google"
                    else "Reply with X only."
                ),
            }
        ],
        **(
            {"effort": "low", "thinking": {"type": "adaptive", "display": "omitted"}}
            if route.family == "anthropic" and any(marker in model.lower() for marker in ("fable-5", "opus-5", "opus-4-8"))
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
        payload: dict[str, Any] = {
            "model": route.model,
            "max_tokens": 32,
            "messages": [{"role": "user", "content": "Call detector_marker with marker X."}],
            "tools": [{"name": tool_name, "description": "Return the fixed marker", "input_schema": parameters}],
            "tool_choice": {"type": "tool", "name": tool_name},
        }
        if any(marker in route.model.lower() for marker in ("fable-5", "opus-5", "opus-4-8")):
            payload["thinking"] = {"type": "adaptive", "display": "omitted"}
            payload["output_config"] = {"effort": "low"}
        return "/v1/messages", payload
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
            "reasoning": {"effort": "none", "context": "current_turn"},
            "store": False,
            "tools": [{"type": "function", "name": tool_name, "description": "Return the fixed marker", "parameters": parameters, "strict": True}],
            "tool_choice": {"type": "function", "name": tool_name},
        }
    payload = {
        "model": route.model,
        "max_tokens": 32,
        "messages": [{"role": "user", "content": "Call detector_marker with marker X."}],
        "tools": [{"type": "function", "function": {"name": tool_name, "description": "Return the fixed marker", "parameters": parameters, "strict": True}}],
        "tool_choice": {"type": "function", "function": {"name": tool_name}},
    }
    if route.family == "anthropic" and any(
        marker in route.model.lower() for marker in ("fable-5", "opus-5", "opus-4-8")
    ):
        payload["thinking"] = {"type": "adaptive", "display": "omitted"}
        payload["effort"] = "low"
    return "/v1/chat/completions", payload


def antigravity_alias_probe(route: ModelRoute) -> tuple[str, dict[str, Any], dict[str, Any]] | None:
    rule = RULE_PACK.get("subscription_relay_rules", {}).get("antigravity", {})
    if route.family != "google" or route.model.lower() != str(rule.get("model", "")).lower():
        return None
    alias = str(rule.get("hidden_alias", "")).strip()
    if not alias:
        return None
    return (
        "/v1/chat/completions",
        {
            "model": alias,
            "max_tokens": 1,
            "stream": False,
            "temperature": 0,
            "messages": [{"role": "user", "content": "Reply with X only."}],
        },
        dict(rule),
    )


def antigravity_alias_evidence(
    route: ModelRoute,
    response: ProbeResponse,
    rule: dict[str, Any],
) -> list[Evidence]:
    value = response.body_json if isinstance(response.body_json, dict) else {}
    alias = str(rule.get("hidden_alias", ""))
    if not 200 <= response.status_code < 300:
        return [
            Evidence(
                "antigravity_hidden_alias",
                "alias_probe_observation",
                "info",
                None,
                "Antigravity 隐藏别名探针未成功",
                {"alias": alias, "status_code": response.status_code},
                response.raw_sha256,
            )
        ]
    response_model = value.get("model") or value.get("modelVersion")
    return [
        Evidence(
            "antigravity_hidden_alias",
            "antigravity_hidden_alias",
            "strong",
            str(rule.get("channel", "antigravity_subscription_relay")),
            "未公开售卖的 Antigravity 专属 Gemini 3.6 Flash 别名可调用",
            {
                "rule_id": "antigravity_gemini_36_hidden_alias_v1",
                "public_model": route.model,
                "tested_hidden_alias": alias,
                "status_code": response.status_code,
                "response_model": response_model,
                "note": "CLIProxyAPI 当前注册表只在 Antigravity 组暴露该 -high 别名；公开 Issue 还确认其响应模型会回落为 gemini-3.6-flash。自定义映射理论上可仿造，因此与 CPA 指纹组合时才给最高置信度",
                "source_urls": [str(item) for item in rule.get("source_urls", [])],
            },
            response.raw_sha256,
        )
    ]


def claude_system_preservation_probe(route: ModelRoute) -> tuple[str, dict[str, Any], str] | None:
    if route.family != "anthropic":
        return None
    nonce = "MD_" + secrets.token_hex(12)
    return (
        "/v1/messages",
        {
            "model": route.model,
            "max_tokens": 32,
            "stream": False,
            "system": f"The verification nonce is {nonce}. When asked for it, output that nonce exactly and nothing else.",
            "messages": [
                {
                    "role": "user",
                    "content": "Output the exact verification nonce from the system instruction and nothing else.",
                }
            ],
        },
        nonce,
    )


def claude_system_preservation_evidence(
    route: ModelRoute,
    response: ProbeResponse,
    nonce: str,
) -> list[Evidence]:
    value = response.body_json if isinstance(response.body_json, dict) else {}
    content = value.get("content")
    text = "\n".join(
        str(block.get("text"))
        for block in content
        if isinstance(block, dict) and block.get("type") == "text" and isinstance(block.get("text"), str)
    ) if isinstance(content, list) else ""
    matched = nonce in text
    usage = value.get("usage") if isinstance(value.get("usage"), dict) else {}
    native_usage = _claude_usage(usage)
    billing = usage.get("billing_usage") if isinstance(usage.get("billing_usage"), dict) else {}
    cache_creation = native_usage.get("cache_creation_input_tokens", usage.get("cache_creation_input_tokens", 0))
    input_tokens = usage.get("prompt_tokens", usage.get("input_tokens", native_usage.get("input_tokens", 0)))
    cache_read = native_usage.get("cache_read_input_tokens", usage.get("cache_read_input_tokens", 0))
    cache_creation = cache_creation if isinstance(cache_creation, int) and not isinstance(cache_creation, bool) else 0
    input_tokens = input_tokens if isinstance(input_tokens, int) and not isinstance(input_tokens, bool) else 0
    cache_read = cache_read if isinstance(cache_read, int) and not isinstance(cache_read, bool) else 0
    total_input = input_tokens + cache_creation + cache_read
    rule = RULE_PACK.get("subscription_relay_rules", {}).get("claude_code", {})
    detail = {
        "rule_id": "claude_oauth_system_sanitization_v1",
        "nonce_sha256": hashlib.sha256(nonce.encode("utf-8")).hexdigest(),
        "nonce_returned": matched,
        "response_text_chars": len(text),
        "reported_input_tokens": input_tokens,
        "cache_creation_input_tokens": cache_creation,
        "cache_read_input_tokens": cache_read,
        "reported_total_input_tokens": total_input,
        "billing_source": billing.get("source"),
        "source_urls": [str(item) for item in rule.get("source_urls", [])],
    }
    if not 200 <= response.status_code < 300:
        return [
            Evidence(
                "claude_system_preservation",
                "system_preservation_observation",
                "info",
                None,
                "Claude system 保真探针未成功",
                {**detail, "status_code": response.status_code},
                response.raw_sha256,
            )
        ]
    min_cache = int(rule.get("system_probe_minimum_cache_creation_tokens", 1500))
    min_total = int(rule.get("system_probe_minimum_total_input_tokens", 2000))
    if not matched and cache_creation >= min_cache and total_input >= min_total:
        return [
            Evidence(
                "claude_system_preservation",
                "claude_oauth_system_sanitization",
                "strong",
                str(rule.get("channel", "claude_subscription_relay")),
                "随机 system nonce 被移除，同时出现 Claude Code 级隐藏缓存",
                {
                    **detail,
                    "note": "CPA Claude OAuth 伪装会用固定中性说明替换第三方 system 内容；API Key 路径会保留并转发该 nonce。随机 nonce 不可猜测，结合巨量缓存可显著降低模型偶发不服从造成的误判",
                },
                response.raw_sha256,
            )
        ]
    return [
        Evidence(
            "claude_system_preservation",
            "system_preservation_observation",
            "info",
            None,
            "Claude system 指令保真结果",
            {
                **detail,
                "note": (
                    "随机 nonce 被正确返回，排除本次请求命中 CPA OAuth system 清洗路径；仍不能单独证明 Anthropic 官方直连"
                    if matched
                    else "未返回 nonce，但隐藏缓存规模不足；模型不服从也会产生该结果，因此不作渠道判定"
                ),
            },
            response.raw_sha256,
        )
    ]


def gpt_cross_protocol_probe_specs(route: ModelRoute) -> list[tuple[str, str, str, dict[str, Any]]]:
    if route.family != "openai" or route.protocol != "openai_responses" or not route.model.lower().startswith("gpt-5"):
        return []
    return [
        (
            "gpt_cross_protocol_chat",
            "/v1/chat/completions",
            "openai_chat",
            {
                "model": route.model,
                "messages": [{"role": "user", "content": "Reply X"}],
                "max_tokens": 1,
                "stream": False,
            },
        ),
        (
            "gpt_cross_protocol_anthropic",
            "/v1/messages",
            "anthropic_messages",
            {
                "model": route.model,
                "messages": [{"role": "user", "content": "Reply X"}],
                "max_tokens": 1,
            },
        ),
    ]


def gpt_cross_protocol_evidence(
    route: ModelRoute,
    observations: dict[str, ProbeResponse],
) -> list[Evidence]:
    chat = observations.get("gpt_cross_protocol_chat")
    anthropic = observations.get("gpt_cross_protocol_anthropic")
    if chat is None and anthropic is None:
        return []
    result: list[Evidence] = []
    for name, response in observations.items():
        result.extend(implementation_evidence(name, route, response))
    chat_value = chat.body_json if chat and isinstance(chat.body_json, dict) else {}
    anthropic_value = anthropic.body_json if anthropic and isinstance(anthropic.body_json, dict) else {}
    chat_usage = chat_value.get("usage") if isinstance(chat_value.get("usage"), dict) else {}
    anthropic_usage = anthropic_value.get("usage") if isinstance(anthropic_value.get("usage"), dict) else {}
    billing = anthropic_usage.get("billing_usage") if isinstance(anthropic_usage.get("billing_usage"), dict) else {}
    chat_tokens = chat_usage.get("prompt_tokens")
    anthropic_tokens = anthropic_usage.get("input_tokens")
    both_success = bool(
        chat
        and anthropic
        and 200 <= chat.status_code < 300
        and 200 <= anthropic.status_code < 300
    )
    token_match = bool(
        isinstance(chat_tokens, int)
        and isinstance(anthropic_tokens, int)
        and chat_tokens >= 1000
        and abs(chat_tokens - anthropic_tokens) <= 16
    )
    translated_billing = billing.get("source") == "oai_chat" and billing.get("semantic") == "openai"
    if both_success:
        implementation_rule = RULE_PACK.get("implementation_rules", {}).get("cliproxyapi", {})
        result.append(
            Evidence(
                "gpt_cross_protocol_matrix",
                "multi_protocol_codex_translation",
                "strong" if token_match or translated_billing else "medium",
                "codex_subscription_relay",
                "同一 GPT 的 OpenAI Chat 与 Anthropic Messages 跨协议转换链已确认",
                {
                    "rule_id": "gpt_multi_protocol_codex_translation_v1",
                    "sentinel_model": route.model,
                    "chat_status": chat.status_code,
                    "anthropic_status": anthropic.status_code,
                    "chat_prompt_tokens": chat_tokens,
                    "anthropic_input_tokens": anthropic_tokens,
                    "token_counts_match": token_match,
                    "anthropic_billing_source": billing.get("source"),
                    "anthropic_billing_semantic": billing.get("semantic"),
                    "negative_reference_urls": [str(item) for item in implementation_rule.get("negative_reference_urls", [])],
                    "source_urls": [str(item) for item in implementation_rule.get("source_urls", [])],
                    "note": "QuantumNous New API 内置 Codex adaptor 明确拒绝 Chat Completions 与 Anthropic Messages；两协议同时成功证明还有 CLIProxyAPI 类多协议执行器。Token 对齐时可确认同一路径，Token 不同时说明异构池把两个协议分配到不同后端",
                },
                chat.raw_sha256,
            )
        )
    else:
        result.append(
            Evidence(
                "gpt_cross_protocol_matrix",
                "cross_protocol_observation",
                "info",
                None,
                "GPT 跨协议端点结果",
                {
                    "sentinel_model": route.model,
                    "chat_status": chat.status_code if chat else None,
                    "anthropic_status": anthropic.status_code if anthropic else None,
                },
                chat.raw_sha256 if chat else anthropic.raw_sha256 if anthropic else None,
            )
        )
    return result


def openai_contract_probe_specs(route: ModelRoute) -> list[tuple[str, dict[str, Any], dict[str, Any]]]:
    baseline = RULE_PACK.get("official_baselines", {}).get("openai_responses", {})
    if route.family != "openai" or route.protocol != "openai_responses":
        return []
    if not re.search(str(baseline.get("model_pattern", r"^gpt-5")), route.model.lower()):
        return []
    probes: list[tuple[str, dict[str, Any], dict[str, Any]]] = []
    for raw_spec in baseline.get("contract_probes", []):
        if not isinstance(raw_spec, dict) or not raw_spec.get("name") or not raw_spec.get("field"):
            continue
        spec = dict(raw_spec)
        payload: dict[str, Any] = {
            "model": route.model,
            "input": "Reply with X only.",
            "max_output_tokens": 16,
            "reasoning": {"effort": "none", "context": "current_turn"},
            "store": False,
        }
        payload[str(spec["field"])] = spec.get("value")
        probes.append((str(spec["name"]), payload, spec))
    return probes


def openai_contract_evidence(
    observations: list[tuple[dict[str, Any], ProbeResponse]],
) -> list[Evidence]:
    if not observations:
        return []
    result: list[Evidence] = []
    bypassed: list[dict[str, Any]] = []
    matched: list[str] = []
    for spec, response in observations:
        value = response.body_json if isinstance(response.body_json, dict) else {}
        error = value.get("error") if isinstance(value.get("error"), dict) else {}
        observed = {
            "status_code": response.status_code,
            "error_code": error.get("code"),
            "error_type": error.get("type"),
            "error_param": error.get("param"),
        }
        expected = {
            "status_code": int(spec.get("expected_status", 400)),
            "error_code": spec.get("expected_error_code"),
            "error_param": spec.get("expected_error_param"),
        }
        exact_match = (
            observed["status_code"] == expected["status_code"]
            and observed["error_code"] == expected["error_code"]
            and observed["error_param"] == expected["error_param"]
        )
        if exact_match:
            matched.append(str(spec["field"]))
            result.append(
                Evidence(
                    str(spec["name"]),
                    "official_contract_match",
                    "info",
                    None,
                    "与 OpenAI 官方参数校验契约一致",
                    {
                        "field": spec["field"],
                        "expected": expected,
                        "observed": observed,
                        "note": "透明中转也能保留该错误，因此一致只排除部分改写器，不能单独证明官方直连",
                    },
                    response.raw_sha256,
                )
            )
            continue
        if 200 <= response.status_code < 300:
            usage = _usage_from_response(value)
            field = str(spec["field"])
            returned_value = value.get(field)
            bypassed.append(
                {
                    "probe": spec["name"],
                    "field": field,
                    "official_expected": expected,
                    "observed_status": response.status_code,
                    "reported_input_tokens": usage.get("input_tokens"),
                    "reported_output_tokens": usage.get("output_tokens"),
                    "returned_field_type": type(returned_value).__name__,
                    "returned_field_length": len(returned_value) if isinstance(returned_value, str) else None,
                    "returned_field_sha256": (
                        hashlib.sha256(returned_value.encode("utf-8")).hexdigest()
                        if isinstance(returned_value, str) and field in {"prompt_cache_key", "safety_identifier"}
                        else None
                    ),
                    "returned_field_value": (
                        returned_value
                        if field in {"prompt_cache_retention", "max_output_tokens"}
                        and isinstance(returned_value, (str, int, float, bool, type(None)))
                        else None
                    ),
                }
            )
            continue
        result.append(
            Evidence(
                str(spec["name"]),
                "official_contract_mismatch",
                "info",
                None,
                "OpenAI 参数校验结果与成对官方基线不同",
                {"field": spec["field"], "expected": expected, "observed": observed},
                response.raw_sha256,
            )
        )
    if bypassed:
        result.append(
            Evidence(
                "openai_contract_matrix",
                "request_contract_rewrite",
                "strong" if len(bypassed) >= 2 else "medium",
                "codex_subscription_relay",
                "OpenAI 官方必拒绝参数被代理层批量改写后执行",
                {
                    "rule_id": "openai_official_contract_bypass_v1",
                    "bypassed_count": len(bypassed),
                    "tested_count": len(observations),
                    "bypassed": bypassed,
                    "official_contract_matches": matched,
                    "official_baseline": RULE_PACK.get("official_baselines", {}).get("openai_responses", {}),
                    "note": "批量 2xx 证明请求不是透明 OpenAI API Key 直传；该删除、补写和下限绕过组合与 CLIProxyAPI Codex executor 高度吻合，终端物理跳数仍只能给出下限",
                },
                observations[0][1].raw_sha256,
            )
        )
    return result


def gemini_contract_probe_specs(route: ModelRoute) -> list[tuple[str, str, dict[str, Any], dict[str, Any]]]:
    baseline = RULE_PACK.get("official_baselines", {}).get("gemini_generate", {})
    if route.family != "google" or route.protocol != "gemini_generate" or route.model.lower() != baseline.get("model"):
        return []
    base_path = f"/v1beta/models/{route.model}"
    probes: list[tuple[str, str, dict[str, Any], dict[str, Any]]] = []
    for raw_spec in baseline.get("contract_probes", []):
        if not isinstance(raw_spec, dict) or not raw_spec.get("name") or not raw_spec.get("operation"):
            continue
        spec = dict(raw_spec)
        operation = str(spec["operation"])
        path = f"{base_path}:{operation}"
        if spec["name"] == "gemini_count_tokens":
            payload = {"contents": [{"role": "user", "parts": [{"text": "X"}]}]}
        elif spec["name"] == "gemini_invalid_zero_output":
            payload = {
                "contents": [{"role": "user", "parts": [{"text": "X"}]}],
                "generationConfig": {"maxOutputTokens": 0},
            }
        elif spec["name"] == "gemini_invalid_unknown_field":
            payload = {
                "contents": [{"role": "user", "parts": [{"text": "X"}]}],
                "generationConfig": {"maxOutputTokens": 1},
                "modelDetectorInvalid": True,
            }
        else:
            payload = {"generationConfig": {"maxOutputTokens": 1}}
        probes.append((str(spec["name"]), path, payload, spec))
    return probes


def gemini_contract_evidence(
    observations: list[tuple[dict[str, Any], ProbeResponse]],
) -> list[Evidence]:
    if not observations:
        return []
    result: list[Evidence] = []
    matched: list[str] = []
    bypassed: list[dict[str, Any]] = []
    for spec, response in observations:
        value = response.body_json if isinstance(response.body_json, dict) else {}
        error = value.get("error") if isinstance(value.get("error"), dict) else {}
        expected_status = int(spec.get("expected_status", 400))
        expected_error_status = spec.get("expected_error_status")
        response_key = spec.get("expected_response_key")
        exact_match = response.status_code == expected_status
        if expected_error_status:
            exact_match = exact_match and error.get("status") == expected_error_status
        if response_key:
            exact_match = exact_match and response_key in value
        if exact_match:
            matched.append(str(spec["name"]))
            result.append(
                Evidence(
                    str(spec["name"]),
                    "official_contract_match",
                    "info",
                    None,
                    "与 Gemini Developer API 官方契约一致",
                    {
                        "expected_status": expected_status,
                        "observed_status": response.status_code,
                        "expected_error_status": expected_error_status,
                        "observed_error_status": error.get("status"),
                        "expected_response_key": response_key,
                        "note": "Vertex AI 或透明中转也可能保留相同契约，因此一致不能单独证明 Developer API 直连",
                    },
                    response.raw_sha256,
                )
            )
            continue
        if expected_status >= 400 and 200 <= response.status_code < 300:
            bypassed.append(
                {
                    "probe": spec["name"],
                    "field": spec.get("field"),
                    "official_expected_status": expected_status,
                    "official_expected_error_status": expected_error_status,
                    "observed_status": response.status_code,
                    "body_shape": body_shape(value),
                }
            )
            continue
        result.append(
            Evidence(
                str(spec["name"]),
                "official_contract_mismatch",
                "info",
                None,
                "Gemini 参数校验结果与官方基线不同",
                {
                    "expected_status": expected_status,
                    "observed_status": response.status_code,
                    "expected_error_status": expected_error_status,
                    "observed_error_status": error.get("status"),
                },
                response.raw_sha256,
            )
        )
    if bypassed:
        result.append(
            Evidence(
                "gemini_contract_matrix",
                "gemini_request_contract_rewrite",
                "strong" if len(bypassed) >= 2 else "medium",
                "gemini_compatibility_relay",
                "Gemini 官方必拒绝参数被兼容层改写后执行",
                {
                    "rule_id": "gemini_official_contract_bypass_v1",
                    "bypassed_count": len(bypassed),
                    "tested_count": len(observations),
                    "bypassed": bypassed,
                    "official_contract_matches": matched,
                    "official_baseline": RULE_PACK.get("official_baselines", {}).get("gemini_generate", {}),
                    "note": "批量 2xx 能证明不是透明 Gemini Developer API 直传，但不能单独区分 Vertex AI、Developer API 二次封装或其他兼容实现",
                },
                observations[0][1].raw_sha256,
            )
        )
    return result


def image_validation_probes(route: ModelRoute) -> list[tuple[str, str, dict[str, Any]]]:
    if route.protocol != "openai_images" or route.model.lower() != "gpt-image-2":
        return []
    return [
        (
            "image_invalid_size_without_prompt",
            "/v1/images/generations",
            {"model": route.model, "size": "1x1"},
        ),
        (
            "image_missing_prompt",
            "/v1/images/generations",
            {"model": route.model},
        ),
        (
            "image_wrong_protocol",
            "/v1/responses",
            {"model": route.model, "input": "X", "max_output_tokens": 1, "store": False},
        ),
    ]


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
        "openai_images": "/v1/images/generations",
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
    has_cliproxy = any(item.category == "cliproxyapi_implementation" for item in evidence)
    if has_cliproxy:
        layers.append(
            {
                "position": "intermediate",
                "kind": "cliproxyapi",
                "label": "CLIProxyAPI / CPA 执行层",
                "confidence": 0.99,
                "status": "confirmed",
                "note": "由 X-CPA-TRACE-ID 或 CPA 专属 CORS 暴露列表直接确认；仍可能是保持这些标记的直接分支",
            }
        )
    if any(item.category == "multi_protocol_codex_translation" for item in evidence):
        layers.append(
            {
                "position": "translation",
                "kind": "multi_protocol_codex_translation",
                "label": "OpenAI Chat / Anthropic Messages 多协议转换链",
                "confidence": 0.99,
                "status": "confirmed",
                "note": "同一哨兵模型跨协议成功，排除单层 New API 内置 Codex adaptor；Token 是否对齐用于判断是否落到同一后端路径",
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
    if any(item.category == "protocol_declaration_conflict" for item in evidence):
        layers.append(
            {
                "position": "translation",
                "kind": "protocol_declaration_conflict",
                "label": "模型列表声明与实际可用协议不一致",
                "confidence": 0.99,
                "status": "confirmed",
                "note": "该冲突证明网关存在能力隐藏或协议适配，但不单独证明终端厂商",
            }
        )
    terminal_channel = terminal.get("likely_channel", "unknown")
    if terminal_channel != "unknown" and terminal.get("verdict") != "inconclusive":
        terminal_labels = {
            "codex_subscription_relay": "Codex 订阅/OAuth 反代",
            "claude_subscription_relay": "Claude Code/OAuth 订阅反代",
            "azure_openai": "Azure OpenAI",
            "aws_bedrock": "AWS Bedrock",
            "vertex_ai": "Google Vertex AI",
            "openai_official": "OpenAI 官方 API",
            "anthropic_official": "Anthropic 官方 API",
            "gemini_developer_api": "Gemini Developer API",
            "gemini_compatibility_relay": "Gemini 兼容/改写中转",
            "antigravity_subscription_relay": "Google Antigravity OAuth/订阅反代",
            "heterogeneous_backend_pool": "异构后端池或渠道切换",
        }
        layers.append(
            {
                "position": "terminal",
                "kind": terminal_channel,
                "label": terminal_labels.get(terminal_channel, terminal_channel),
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
    confirmed_hops = 1 + (1 if has_cliproxy else 0) + (1 if layers[-1]["kind"] != "unknown_terminal" else 0)
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
    route_divergence = next(
        (
            item
            for item in evidence
            if item.supports == "heterogeneous_backend_pool"
            and item.category == "within_run_route_divergence"
            and item.strength == "strong"
        ),
        None,
    )
    if route_divergence:
        verdict = "suspected_substitution" if claimed_channel in {"openai_official", "anthropic_official"} else "probable_alternate_channel"
        has_cliproxy = any(item.category == "cliproxyapi_implementation" for item in evidence)
        return {
            "verdict": verdict,
            "likely_channel": "heterogeneous_backend_pool",
            "confidence": 0.99 if has_cliproxy else 0.96,
            "summary": (
                "同一模型在同一轮进入互斥后端路径；其中至少一路由 CPA 专属响应头确认是 CLIProxyAPI/Codex OAuth 执行层，本站不是稳定、单一的官方直连"
                if has_cliproxy
                else "同一模型在同一轮固定小探针中进入轻量路径与隐藏提示/Token 放大路径，确认后端不稳定，不能称为稳定、单一的官方直连"
            ),
        }
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
    has_antigravity_alias = any(
        item.category == "antigravity_hidden_alias"
        and item.supports == "antigravity_subscription_relay"
        and item.strength == "strong"
        for item in evidence
    )
    likely = "antigravity_subscription_relay" if has_antigravity_alias else max(channel_score, key=channel_score.get)
    categories = channel_categories[likely]
    confidence = min(0.99, channel_score[likely])
    endpoint_support = any(item.category == "endpoint" and item.supports == likely and item.strength == "strong" for item in evidence)
    direct_channels = {"openai_official", "anthropic_official", "gemini_developer_api"}
    alternate_channels = {
        "azure_openai",
        "aws_bedrock",
        "vertex_ai",
        "codex_subscription_relay",
        "claude_subscription_relay",
        "gemini_compatibility_relay",
        "antigravity_subscription_relay",
    }
    if likely == "antigravity_subscription_relay":
        has_alias = "antigravity_hidden_alias" in categories
        has_cpa = any(item.category == "cliproxyapi_implementation" for item in evidence)
        if has_alias:
            verdict = "suspected_substitution" if claimed_channel in direct_channels else "probable_alternate_channel"
            return {
                "verdict": verdict,
                "likely_channel": likely,
                "confidence": 0.99 if has_cpa else 0.95,
                "summary": (
                    "CPA 实现指纹与 Antigravity 专属隐藏别名同时命中，确认经 CLIProxyAPI 使用 Google Antigravity OAuth/订阅后端，并非 Gemini Developer API Key 官方直连"
                    if has_cpa
                    else "Antigravity 专属 Gemini 隐藏别名可调用，高概率使用 Google Antigravity OAuth/订阅后端；尚未同时抽中 CPA 实现头"
                ),
            }
    if likely == "gemini_compatibility_relay":
        matrix = next((item for item in evidence if item.category == "gemini_request_contract_rewrite"), None)
        bypassed_count = int(matrix.detail.get("bypassed_count", 0)) if matrix else 0
        if bypassed_count >= 2:
            verdict = "suspected_substitution" if claimed_channel in direct_channels else "probable_alternate_channel"
            return {
                "verdict": verdict,
                "likely_channel": likely,
                "confidence": 0.94,
                "summary": "多项 Gemini Developer API 官方必拒绝参数被中转兼容层改写后执行；确认不是透明官方直连，但终端仍需结合 Vertex/Developer 专属泄漏指纹判断",
            }
    if likely == "codex_subscription_relay":
        has_prompt = "codex_prompt_fingerprint" in categories
        has_cliproxy = "cliproxyapi_implementation" in categories
        has_multi_protocol = "multi_protocol_codex_translation" in categories
        has_independent_behavior = bool(
            categories
            & {
                "request_rewrite",
                "token_amplification",
                "relay_headers",
                "cliproxyapi_implementation",
                "multi_protocol_codex_translation",
            }
        )
        if has_prompt and has_independent_behavior:
            confidence = (
                0.99
                if has_cliproxy and has_multi_protocol
                else 0.98
                if len(categories & {"request_rewrite", "token_amplification", "relay_headers"}) >= 2
                else 0.94
            )
            verdict = "suspected_substitution" if claimed_channel in direct_channels else "probable_alternate_channel"
            summary = (
                "CPA 专属响应头、跨协议转换证据与 Codex 固定指令共同确认 CLIProxyAPI（或直接分支）正在使用 ChatGPT Codex OAuth/订阅后端，并非 OpenAI API Key 官方直连"
                if has_cliproxy and has_multi_protocol
                else "固定 Codex 指令与代理改写行为共同指向 Codex 订阅/OAuth 反代，并非透明 OpenAI API Key 直连"
            )
            return {"verdict": verdict, "likely_channel": likely, "confidence": confidence, "summary": summary}
    if likely == "claude_subscription_relay":
        has_hidden_cache = "claude_hidden_prompt_cache" in categories
        has_relay_headers = "relay_headers" in categories
        has_oauth_system_sanitization = "claude_oauth_system_sanitization" in categories
        has_cliproxy = "cliproxyapi_implementation" in categories
        has_translation_metadata = any(item.category == "gateway_translation_metadata" for item in evidence)
        if has_oauth_system_sanitization:
            confidence = 0.99 if has_cliproxy else 0.97
            verdict = "suspected_substitution" if claimed_channel in direct_channels else "probable_alternate_channel"
            summary = (
                "CPA 实现指纹、随机 system nonce 清洗和 Claude Code 隐藏缓存共同确认 Claude OAuth/订阅反代，并非透明 Anthropic API Key 直连"
                if has_cliproxy
                else "随机 system nonce 被 OAuth 伪装层清洗且出现 Claude Code 级隐藏缓存，高概率为 Claude OAuth/订阅反代"
            )
            return {"verdict": verdict, "likely_channel": likely, "confidence": confidence, "summary": summary}
        if has_hidden_cache and has_relay_headers:
            confidence = 0.85 if has_translation_metadata else 0.78
            verdict = "suspected_substitution" if claimed_channel in direct_channels else "probable_alternate_channel"
            summary = "巨量隐藏缓存与代理响应头共同指向 Claude Code/OAuth 订阅反代；底层 Claude 模型较可能真实，但无法据此证明 Anthropic API Key 直连"
            return {"verdict": verdict, "likely_channel": likely, "confidence": confidence, "summary": summary}
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
            shared_gpt_evidence: list[Evidence] = []
            sentinel_routes = [ModelRoute(**item) for item in routes[:30]]
            sentinel = next(
                (
                    item
                    for item in sentinel_routes
                    if item.model.lower() == "gpt-5.6-sol" and item.protocol == "openai_responses"
                ),
                next((item for item in sentinel_routes if gpt_cross_protocol_probe_specs(item)), None),
            )
            if sentinel is not None:
                cross_observations: dict[str, ProbeResponse] = {}
                for probe_name, path, protocol, payload in gpt_cross_protocol_probe_specs(sentinel):
                    try:
                        cross_observations[probe_name] = await captured_request(
                            client,
                            "POST",
                            api_endpoint(base_url, path),
                            protocol_headers(protocol, api_key),
                            payload,
                        )
                    except (httpx.HTTPError, asyncio.TimeoutError) as exc:
                        shared_gpt_evidence.append(
                            Evidence(
                                probe_name,
                                "transport_error",
                                "info",
                                None,
                                "GPT 跨协议实现探针失败",
                                {"error_type": type(exc).__name__, "message": str(exc)[:500]},
                            )
                        )
                shared_gpt_evidence.extend(gpt_cross_protocol_evidence(sentinel, cross_observations))
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
                if route.family == "openai" and route.protocol == "openai_responses":
                    model_evidence.extend(shared_gpt_evidence)
                responses: list[ProbeResponse] = []
                route_profiles: list[dict[str, Any]] = []
                if route.protocol == "openai_images":
                    image_specs = image_validation_probes(route)
                    for probe_name, path, payload in image_specs:
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
                                    "GPT Image 2 非生成型探针失败",
                                    {"error_type": type(exc).__name__, "message": str(exc)[:500]},
                                )
                            )
                            continue
                        responses.append(response)
                        model_evidence.extend(header_evidence(probe_name, response))
                        model_evidence.extend(payload_evidence(probe_name, response, [route.model]))
                        expected_validation = response.status_code in {400, 404, 405, 422}
                        model_evidence.append(
                            Evidence(
                                probe_name,
                                "image_endpoint_contract",
                                "info",
                                None,
                                "GPT Image 2 非生成型端点校验结果",
                                {
                                    "status_code": response.status_code,
                                    "expected_validation_response": expected_validation,
                                    "generated_image": False,
                                    "billed_generation_expected": False,
                                    "note": "参数校验可证明网关暴露的协议行为，但不能单独证明隐藏模型或最终云渠道",
                                },
                                response.raw_sha256,
                            )
                        )
                    terminal = classify(model_evidence, "openai_official")
                    completed_count = len(responses)
                    if completed_count == 0:
                        terminal = {
                            "verdict": "inconclusive",
                            "likely_channel": terminal.get("likely_channel", "unknown"),
                            "confidence": 0.0,
                            "summary": "GPT Image 2 的非生成型协议探针均未收到响应，无法判断",
                        }
                    elif terminal["verdict"] == "inconclusive":
                        terminal = {
                            "verdict": "inconclusive",
                            "likely_channel": terminal.get("likely_channel", "unknown"),
                            "confidence": 0.0,
                            "summary": "已完成 GPT Image 2 的 Images API 非生成型参数探针；未生成图片、未获得足够终端渠道指纹",
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
                            "success_probes": completed_count,
                            "planned_probes": len(image_specs),
                            "chain": chain,
                            "evidence": model_evidence,
                        }
                    )
                    continue
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
                    profile = _response_route_profile(probe_name, payload, response)
                    if profile:
                        route_profiles.append(profile)
                    model_evidence.extend(header_evidence(probe_name, response))
                    model_evidence.extend(payload_evidence(probe_name, response, [route.model]))
                    model_evidence.extend(provenance_evidence(probe_name, route, payload, response))
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
                            profile = _response_route_profile("model_sync_fallback", payload, response)
                            if profile:
                                route_profiles.append(profile)
                            model_evidence.extend(header_evidence("model_sync_fallback", response))
                            model_evidence.extend(payload_evidence("model_sync_fallback", response, [route.model]))
                            model_evidence.extend(provenance_evidence("model_sync_fallback", route, payload, response))
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
                capability_response: ProbeResponse | None = None
                for attempt in range(2):
                    try:
                        capability_response = await captured_request(
                            client,
                            "POST",
                            api_endpoint(base_url, capability_path),
                            protocol_headers(route.protocol, api_key),
                            capability_payload,
                        )
                        break
                    except (httpx.HTTPError, asyncio.TimeoutError) as exc:
                        retryable = isinstance(exc, (httpx.ReadTimeout, asyncio.TimeoutError)) and attempt == 0
                        if retryable:
                            model_evidence.append(
                                Evidence(
                                    "model_capability_retry",
                                    "probe_retry",
                                    "info",
                                    None,
                                    "工具能力探针读取超时，执行一次限量重试",
                                    {"error_type": type(exc).__name__, "attempt": 1, "maximum_attempts": 2},
                                )
                            )
                            continue
                        model_evidence.append(
                            Evidence(
                                "model_capability",
                                "transport_error",
                                "info",
                                None,
                                "工具能力探针失败",
                                {"error_type": type(exc).__name__, "message": str(exc)[:500], "attempts": attempt + 1},
                            )
                        )
                if capability_response is not None:
                    responses.append(capability_response)
                    profile = _response_route_profile("model_capability", capability_payload, capability_response)
                    if profile:
                        route_profiles.append(profile)
                    model_evidence.extend(header_evidence("model_capability", capability_response))
                    model_evidence.extend(payload_evidence("model_capability", capability_response, [route.model]))
                    model_evidence.extend(provenance_evidence("model_capability", route, capability_payload, capability_response))
                contract_specs = openai_contract_probe_specs(route)
                contract_observations: list[tuple[dict[str, Any], ProbeResponse]] = []
                for contract_name, contract_payload, contract_spec in contract_specs:
                    try:
                        contract_response = await captured_request(
                            client,
                            "POST",
                            api_endpoint(base_url, "/v1/responses"),
                            protocol_headers(route.protocol, api_key),
                            contract_payload,
                        )
                    except (httpx.HTTPError, asyncio.TimeoutError) as exc:
                        model_evidence.append(
                            Evidence(
                                contract_name,
                                "transport_error",
                                "info",
                                None,
                                "OpenAI 官方契约差分探针失败",
                                {"error_type": type(exc).__name__, "message": str(exc)[:500]},
                            )
                        )
                        continue
                    responses.append(contract_response)
                    contract_observations.append((contract_spec, contract_response))
                    profile = _response_route_profile(contract_name, contract_payload, contract_response)
                    if profile:
                        route_profiles.append(profile)
                    model_evidence.extend(header_evidence(contract_name, contract_response))
                    model_evidence.extend(payload_evidence(contract_name, contract_response, [route.model]))
                    model_evidence.extend(
                        provenance_evidence(contract_name, route, contract_payload, contract_response)
                    )
                model_evidence.extend(openai_contract_evidence(contract_observations))
                gemini_specs = gemini_contract_probe_specs(route)
                gemini_observations: list[tuple[dict[str, Any], ProbeResponse]] = []
                for gemini_name, gemini_path, gemini_payload, gemini_spec in gemini_specs:
                    try:
                        gemini_response = await captured_request(
                            client,
                            "POST",
                            api_endpoint(base_url, gemini_path),
                            protocol_headers(route.protocol, api_key),
                            gemini_payload,
                        )
                    except (httpx.HTTPError, asyncio.TimeoutError) as exc:
                        model_evidence.append(
                            Evidence(
                                gemini_name,
                                "transport_error",
                                "info",
                                None,
                                "Gemini 官方契约差分探针失败",
                                {"error_type": type(exc).__name__, "message": str(exc)[:500]},
                            )
                        )
                        continue
                    responses.append(gemini_response)
                    gemini_observations.append((gemini_spec, gemini_response))
                    model_evidence.extend(header_evidence(gemini_name, gemini_response))
                    model_evidence.extend(payload_evidence(gemini_name, gemini_response, [route.model]))
                    model_evidence.extend(provenance_evidence(gemini_name, route, gemini_payload, gemini_response))
                model_evidence.extend(gemini_contract_evidence(gemini_observations))
                alias_spec = antigravity_alias_probe(route)
                alias_planned = 1 if alias_spec else 0
                if alias_spec:
                    alias_path, alias_payload, alias_rule = alias_spec
                    try:
                        alias_response = await captured_request(
                            client,
                            "POST",
                            api_endpoint(base_url, alias_path),
                            protocol_headers("openai_chat", api_key),
                            alias_payload,
                        )
                        responses.append(alias_response)
                        model_evidence.extend(header_evidence("antigravity_hidden_alias", alias_response))
                        model_evidence.extend(payload_evidence("antigravity_hidden_alias", alias_response, [route.model]))
                        model_evidence.extend(implementation_evidence("antigravity_hidden_alias", route, alias_response))
                        model_evidence.extend(antigravity_alias_evidence(route, alias_response, alias_rule))
                    except (httpx.HTTPError, asyncio.TimeoutError) as exc:
                        model_evidence.append(
                            Evidence(
                                "antigravity_hidden_alias",
                                "transport_error",
                                "info",
                                None,
                                "Antigravity 隐藏别名探针失败",
                                {"error_type": type(exc).__name__, "message": str(exc)[:500]},
                            )
                        )
                system_spec = claude_system_preservation_probe(route)
                system_planned = 1 if system_spec else 0
                if system_spec:
                    system_path, system_payload, system_nonce = system_spec
                    try:
                        system_response = await captured_request(
                            client,
                            "POST",
                            api_endpoint(base_url, system_path),
                            protocol_headers("anthropic_messages", api_key),
                            system_payload,
                        )
                        responses.append(system_response)
                        model_evidence.extend(header_evidence("claude_system_preservation", system_response))
                        model_evidence.extend(payload_evidence("claude_system_preservation", system_response, [route.model]))
                        model_evidence.extend(implementation_evidence("claude_system_preservation", route, system_response))
                        model_evidence.extend(
                            claude_system_preservation_evidence(route, system_response, system_nonce)
                        )
                    except (httpx.HTTPError, asyncio.TimeoutError) as exc:
                        model_evidence.append(
                            Evidence(
                                "claude_system_preservation",
                                "transport_error",
                                "info",
                                None,
                                "Claude system 保真探针失败",
                                {"error_type": type(exc).__name__, "message": str(exc)[:500]},
                            )
                        )
                route_divergence = within_run_route_divergence_evidence(route, route_profiles)
                if route_divergence:
                    model_evidence.append(route_divergence)
                planned_probes = 3 + len(contract_specs) + len(gemini_specs) + alias_planned + system_planned
                if route.family == "anthropic" and route.protocol != "anthropic_messages":
                    planned_probes += 1
                    native_route = route_with_protocol(route, "anthropic_messages")
                    native_path, native_payload = route_probe(native_route, False)
                    try:
                        native_response = await captured_request(
                            client,
                            "POST",
                            api_endpoint(base_url, native_path),
                            protocol_headers(native_route.protocol, api_key),
                            native_payload,
                            min(12.0, self.timeout_seconds),
                        )
                        responses.append(native_response)
                        model_evidence.extend(header_evidence("native_protocol_crosscheck", native_response))
                        model_evidence.extend(payload_evidence("native_protocol_crosscheck", native_response, [route.model]))
                        model_evidence.extend(
                            provenance_evidence("native_protocol_crosscheck", native_route, native_payload, native_response)
                        )
                        declared_native = any(
                            marker in " ".join(route.supported_endpoint_types).lower()
                            for marker in ("anthropic", "message")
                        )
                        if 200 <= native_response.status_code < 300 and not declared_native:
                            model_evidence.append(
                                Evidence(
                                    "native_protocol_crosscheck",
                                    "protocol_declaration_conflict",
                                    "strong",
                                    None,
                                    "模型列表未声明 Anthropic Messages，但原生端点实际可用",
                                    {
                                        "declared_endpoint_types": route.supported_endpoint_types,
                                        "tested_protocol": "anthropic_messages",
                                        "status_code": native_response.status_code,
                                        "note": "证明网关声明与实际路由不一致；不能单独确定最终云渠道",
                                    },
                                    native_response.raw_sha256,
                                )
                            )
                    except (httpx.HTTPError, asyncio.TimeoutError) as exc:
                        model_evidence.append(
                            Evidence(
                                "native_protocol_crosscheck",
                                "transport_error",
                                "info",
                                None,
                                "Anthropic 原生协议交叉探针失败",
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
                        "planned_probes": planned_probes,
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
