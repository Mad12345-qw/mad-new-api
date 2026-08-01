import asyncio
import json
import os
from urllib.parse import urlparse
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any

import httpx
from fastapi import Cookie, Depends, FastAPI, HTTPException, Request, Response
from fastapi.responses import FileResponse
from pydantic import BaseModel, Field, HttpUrl, field_validator

from database import Database, utc_now
from discovery import discover_models, route_for_model, validate_public_api_url
from engine import DetectorEngine, Evidence, RULE_PACK, RULE_VERSION, classify, cliproxyapi_header_fingerprint
from new_api import NewAPIClient, NewAPIIntegrationError
from notifications import send_notifications
from security import decrypt_secret, encrypt_secret, make_session, mask_secret, validate_runtime_secrets, verify_admin_token, verify_session


ROOT = Path(__file__).resolve().parent
validate_runtime_secrets()
db = Database()
engine = DetectorEngine(float(os.environ.get("DETECTOR_TIMEOUT_SECONDS", "30")))
new_api = NewAPIClient()
run_lock = asyncio.Lock()
scheduler_stop = asyncio.Event()
CLAUDE_HISTORY_RULE = RULE_PACK.get("history_rules", {}).get("claude_route_change", {})
CLIPROXY_HISTORY_RULE = RULE_PACK.get("implementation_rules", {}).get("cliproxyapi", {})


class LoginInput(BaseModel):
    token: str = Field(min_length=12, max_length=500)


class ModelRouteInput(BaseModel):
    model: str = Field(min_length=1, max_length=200)
    family: str
    provider: str
    protocol: str
    endpoint: str
    fallbacks: list[str] = Field(default_factory=list)
    supported_endpoint_types: list[str] = Field(default_factory=list)
    owned_by: str = ""
    discovered_via: str = ""
    route_reason: str = ""


class DiscoveryInput(BaseModel):
    base_url: HttpUrl
    api_key: str = Field(min_length=1, max_length=1000)


class UpstreamInput(BaseModel):
    name: str = Field(min_length=1, max_length=100)
    base_url: HttpUrl
    api_style: str = "auto"
    api_key: str = Field(default="", max_length=1000)
    models: list[str] = Field(default_factory=list, max_length=50)
    model_routes: list[ModelRouteInput] = Field(default_factory=list, max_length=50)
    discovery: dict[str, Any] = Field(default_factory=dict)
    role: str = "candidate"
    claimed_channel: str = "unknown"
    expected_channel: str = "unknown"
    reference_upstream_id: int | None = None
    allow_paid_probes: bool = False
    enabled: bool = True
    auto_disable_on_mismatch: bool = False

    @field_validator("api_style")
    @classmethod
    def validate_style(cls, value: str) -> str:
        if value not in {"auto", "openai", "anthropic", "gemini"}:
            raise ValueError("api_style must be auto, openai, anthropic, or gemini")
        return value

    @field_validator("role")
    @classmethod
    def validate_role(cls, value: str) -> str:
        if value not in {"candidate", "reference"}:
            raise ValueError("role must be candidate or reference")
        return value

    @field_validator("models")
    @classmethod
    def normalize_models(cls, value: list[str]) -> list[str]:
        return list(dict.fromkeys(item.strip() for item in value if item.strip()))


class RunInput(BaseModel):
    upstream_id: int | None = None
    mode: str = "safe"

    @field_validator("mode")
    @classmethod
    def validate_mode(cls, value: str) -> str:
        if value not in {"safe", "active"}:
            raise ValueError("mode must be safe or active")
        return value


class SettingsInput(BaseModel):
    scheduler_enabled: bool = False
    interval_minutes: int = Field(ge=10, le=1440)
    scheduled_mode: str = "safe"
    webhook_url: str = ""
    webhook_type: str = "generic"
    email_enabled: bool = False
    smtp_host: str = ""
    smtp_port: int = Field(default=587, ge=1, le=65535)
    smtp_username: str = ""
    smtp_password: str = Field(default="", max_length=1000)
    smtp_from: str = ""
    smtp_starttls: bool = True
    smtp_ssl: bool = False
    alert_email_to: str = ""
    auto_disable_enabled: bool = False
    auto_disable_min_confidence: float = Field(default=0.95, ge=0.9, le=0.999)

    @field_validator("scheduled_mode")
    @classmethod
    def validate_scheduled_mode(cls, value: str) -> str:
        if value not in {"safe", "active"}:
            raise ValueError("scheduled_mode must be safe or active")
        return value

    @field_validator("webhook_type")
    @classmethod
    def validate_webhook_type(cls, value: str) -> str:
        if value not in {"generic", "feishu", "dingtalk"}:
            raise ValueError("webhook_type must be generic, feishu, or dingtalk")
        return value


class NewAPIChannelPolicyInput(BaseModel):
    expected_channel: str = "unknown"
    auto_disable_on_mismatch: bool = False
    enabled: bool = True


def require_admin(request: Request, detector_session: str = Cookie(default="")) -> None:
    if verify_session(detector_session):
        return
    internal_url = os.environ.get("DETECTOR_NEW_API_INTERNAL_URL", "").strip()
    cookie = request.headers.get("cookie", "")
    new_api_user = request.headers.get("new-api-user", "").strip()
    if internal_url and cookie and new_api_user.isdigit():
        try:
            response = httpx.get(
                internal_url.rstrip("/") + "/api/user/self",
                headers={"cookie": cookie, "New-Api-User": new_api_user},
                timeout=3,
                follow_redirects=False,
            )
            payload = response.json() if response.status_code == 200 else {}
            role = int(((payload.get("data") or {}).get("role") or 0)) if isinstance(payload, dict) else 0
            returned_id = str(((payload.get("data") or {}).get("id") or "")) if isinstance(payload, dict) else ""
            if payload.get("success") is True and role >= 10 and returned_id == new_api_user:
                return
        except (httpx.HTTPError, ValueError, TypeError):
            pass
    raise HTTPException(status_code=401, detail="New API administrator login or detector token required")


def public_upstream(row: dict[str, Any]) -> dict[str, Any]:
    result = dict(row)
    result.pop("api_key_encrypted", None)
    result["models"] = json.loads(result.pop("models_json", "[]"))
    result["model_routes"] = json.loads(result.pop("model_routes_json", "[]"))
    result["discovery"] = json.loads(result.pop("discovery_json", "{}"))
    result["expected_models"] = json.loads(result.pop("expected_models_json", "[]"))
    for field in ("allow_paid_probes", "enabled", "auto_disable_on_mismatch"):
        result[field] = bool(result[field])
    return result


def public_settings() -> dict[str, Any]:
    settings = db.settings()
    encrypted = str(settings.pop("smtp_password_encrypted", "") or "")
    settings["smtp_password_configured"] = bool(encrypted)
    return settings


def load_upstream(upstream_id: int) -> dict[str, Any]:
    row = db.row("SELECT * FROM upstreams WHERE id=?", (upstream_id,))
    if not row:
        raise HTTPException(status_code=404, detail="upstream not found")
    return row


def routes_for_upstream(upstream: dict[str, Any]) -> list[dict[str, Any]]:
    routes = json.loads(upstream.get("model_routes_json") or "[]")
    if routes:
        return routes
    models = json.loads(upstream.get("models_json") or "[]")
    legacy_style = str(upstream.get("api_style") or "auto")
    supported = [] if legacy_style == "auto" else [legacy_style]
    inferred: list[dict[str, Any]] = []
    for model in models:
        route = route_for_model(str(model), "", supported, "legacy_migration")
        if route:
            inferred.append(route.to_dict())
    return inferred


def normalized_submitted_routes(items: list[ModelRouteInput]) -> list[dict[str, Any]]:
    routes: list[dict[str, Any]] = []
    for item in items:
        route = route_for_model(
            item.model,
            item.owned_by,
            item.supported_endpoint_types,
            item.discovered_via or "submitted_discovery",
        )
        if route:
            routes.append(route.to_dict())
    return routes


KNOWN_EXPECTED_CHANNELS = {
    "unknown",
    "openai_official",
    "anthropic_official",
    "gemini_developer_api",
    "azure_openai",
    "aws_bedrock",
    "vertex_ai",
    "codex_subscription_relay",
    "claude_subscription_relay",
    "antigravity_subscription_relay",
    "relay_or_custom",
}


def expected_channel_for_new_api(channel: dict[str, Any]) -> str:
    channel_type = int(channel.get("type") or 0)
    host = (urlparse(str(channel.get("base_url") or "")).hostname or "").lower()
    if channel_type == 3:
        return "azure_openai"
    if channel_type == 33:
        return "aws_bedrock"
    if channel_type == 41:
        return "vertex_ai"
    if channel_type == 57:
        return "codex_subscription_relay"
    if channel_type == 1 and host in {"api.openai.com"}:
        return "openai_official"
    if channel_type == 14 and host in {"api.anthropic.com"}:
        return "anthropic_official"
    if channel_type == 24 and host in {"generativelanguage.googleapis.com"}:
        return "gemini_developer_api"
    return "unknown"


def channel_endpoint_types(channel_type: int) -> list[str]:
    if channel_type == 14:
        return ["anthropic"]
    if channel_type in {24, 41}:
        return ["gemini"]
    return ["openai"]


def effective_channel_models(channel: dict[str, Any]) -> list[str]:
    models = [str(item).strip() for item in channel.get("models", []) if str(item).strip()]
    mapping = channel.get("model_mapping")
    if not isinstance(mapping, dict):
        mapping = {}
    return list(dict.fromkeys(str(mapping.get(model) or model).strip() for model in models if str(mapping.get(model) or model).strip()))


async def sync_new_api_channels() -> dict[str, Any]:
    channels = await new_api.channels()
    now = utc_now()
    seen: set[tuple[int, int]] = set()
    created = 0
    updated = 0
    ignored = 0
    for channel in channels:
        channel_id = int(channel.get("id") or 0)
        channel_type = int(channel.get("type") or 0)
        if channel_id <= 0:
            ignored += 1
            continue
        models = effective_channel_models(channel)
        routes: list[dict[str, Any]] = []
        endpoint_types = channel_endpoint_types(channel_type)
        for model in models:
            route = route_for_model(model, str(channel.get("type_name") or ""), endpoint_types, "new_api_channel")
            if route:
                routes.append(route.to_dict())
        if not routes:
            ignored += 1
            continue
        expected_models = [item["model"] for item in routes]
        keys = channel.get("keys")
        if not isinstance(keys, list):
            keys = []
        for key_item in keys:
            if not isinstance(key_item, dict):
                continue
            key_index = int(key_item.get("index") or 0)
            api_key = str(key_item.get("key") or "")
            if not api_key:
                continue
            seen.add((channel_id, key_index))
            current = db.row(
                "SELECT * FROM upstreams WHERE new_api_channel_id=? AND new_api_key_index=?",
                (channel_id, key_index),
            )
            auto_expected = expected_channel_for_new_api(channel)
            expected = str(current.get("expected_channel") or "unknown") if current else auto_expected
            if expected == "unknown" and auto_expected != "unknown":
                expected = auto_expected
            channel_enabled = int(channel.get("status") or 0) == 1 and bool(key_item.get("enabled", True))
            display_name = str(channel.get("name") or f"Channel {channel_id}")
            if len(keys) > 1:
                display_name += f" · Key {key_index + 1}"
            values = (
                display_name,
                str(channel.get("base_url") or "").rstrip("/"),
                expected,
                encrypt_secret(api_key),
                mask_secret(api_key),
                json.dumps(expected_models, ensure_ascii=False),
                json.dumps(routes, ensure_ascii=False),
                json.dumps({"source": "new_api", "channel": {"id": channel_id, "type": channel_type}}, ensure_ascii=False),
                int(channel_enabled),
                channel_type,
                int(channel.get("status") or 0),
                expected,
                json.dumps(expected_models, ensure_ascii=False),
                now,
                now,
            )
            if current:
                db.execute(
                    "UPDATE upstreams SET name=?,base_url=?,api_style='auto',claimed_channel=?,api_key_encrypted=?,api_key_masked=?,"
                    "models_json=?,model_routes_json=?,discovery_json=?,allow_paid_probes=1,enabled=?,source_type='new_api',"
                    "new_api_channel_type=?,new_api_channel_status=?,expected_channel=?,expected_models_json=?,last_synced_at=?,updated_at=? "
                    "WHERE id=?",
                    values + (current["id"],),
                )
                updated += 1
            else:
                db.execute(
                    "INSERT INTO upstreams(name,base_url,api_style,claimed_channel,api_key_encrypted,api_key_masked,models_json,"
                    "model_routes_json,discovery_json,role,reference_upstream_id,allow_paid_probes,enabled,source_type,new_api_channel_id,"
                    "new_api_key_index,new_api_channel_type,new_api_channel_status,expected_channel,expected_models_json,"
                    "auto_disable_on_mismatch,last_synced_at,created_at,updated_at) "
                    "VALUES(?,?,'auto',?,?,?,?,?,?,'candidate',NULL,1,?,'new_api',?,?,?, ?,?,?,0,?,?,?)",
                    (
                        display_name,
                        str(channel.get("base_url") or "").rstrip("/"),
                        expected,
                        encrypt_secret(api_key),
                        mask_secret(api_key),
                        json.dumps(expected_models, ensure_ascii=False),
                        json.dumps(routes, ensure_ascii=False),
                        json.dumps({"source": "new_api", "channel": {"id": channel_id, "type": channel_type}}, ensure_ascii=False),
                        int(channel_enabled),
                        channel_id,
                        key_index,
                        channel_type,
                        int(channel.get("status") or 0),
                        expected,
                        json.dumps(expected_models, ensure_ascii=False),
                        now,
                        now,
                        now,
                    ),
                )
                created += 1
    linked = db.rows("SELECT id,new_api_channel_id,new_api_key_index FROM upstreams WHERE source_type='new_api'")
    disabled = 0
    for item in linked:
        marker = (int(item["new_api_channel_id"]), int(item["new_api_key_index"]))
        if marker not in seen:
            db.execute("UPDATE upstreams SET enabled=0,updated_at=? WHERE id=?", (now, item["id"]))
            disabled += 1
    db.set_setting("last_new_api_sync_at", now)
    return {
        "channels_received": len(channels),
        "created": created,
        "updated": updated,
        "disabled_stale": disabled,
        "ignored_without_supported_models": ignored,
        "synced_at": now,
    }


async def save_evidence(run_id: int, evidence: list[Evidence], model: str | None = None) -> None:
    with db.connect() as connection:
        for item in evidence:
            connection.execute(
                "INSERT INTO evidence(run_id,model,probe,category,strength,supports,title,detail_json,raw_sha256,created_at) "
                "VALUES(?,?,?,?,?,?,?,?,?,?)",
                (
                    run_id,
                    model,
                    item.probe,
                    item.category,
                    item.strength,
                    item.supports,
                    item.title,
                    json.dumps(item.detail, ensure_ascii=False, separators=(",", ":")),
                    item.raw_sha256,
                    utc_now(),
                ),
            )


def compare_with_reference(upstream: dict[str, Any], evidence: list[Evidence]) -> Evidence | None:
    reference_id = upstream.get("reference_upstream_id")
    if not reference_id:
        return None
    reference_run = db.row(
        "SELECT id FROM runs WHERE upstream_id=? AND status='completed' ORDER BY started_at DESC LIMIT 1",
        (reference_id,),
    )
    if not reference_run:
        return Evidence(
            "paired_reference",
            "reference_comparison",
            "info",
            None,
            "可信参考尚无可用基线",
            {"reference_upstream_id": reference_id},
        )
    reference_rows = db.rows(
        "SELECT category,strength,supports,title,detail_json FROM evidence WHERE run_id=?",
        (reference_run["id"],),
    )
    candidate_signature = {
        (item.category, item.supports, item.title)
        for item in evidence
        if item.strength in {"strong", "medium"} and item.category in {"headers", "payload", "sse", "errors"}
    }
    reference_signature = {
        (row["category"], row["supports"], row["title"])
        for row in reference_rows
        if row["strength"] in {"strong", "medium"} and row["category"] in {"headers", "payload", "sse", "errors"}
    }
    union = candidate_signature | reference_signature
    similarity = 1.0 if not union else len(candidate_signature & reference_signature) / len(union)
    strength = "medium" if union and similarity < 0.35 else "info"
    title = "候选与可信参考的结构指纹差异较大" if strength == "medium" else "候选与可信参考的结构指纹比较"
    return Evidence(
        "paired_reference",
        "reference_comparison",
        strength,
        None,
        title,
        {
            "reference_upstream_id": reference_id,
            "reference_run_id": reference_run["id"],
            "structural_similarity": round(similarity, 3),
            "candidate_signature_count": len(candidate_signature),
            "reference_signature_count": len(reference_signature),
            "note": "结构相似度只用于佐证，不能单独证明模型身份",
        },
    )


async def notify_if_needed(upstream: dict[str, Any], result: dict[str, Any], run_id: int) -> None:
    if result["verdict"] not in {"probable_alternate_channel", "suspected_substitution"}:
        return
    webhook = str(db.settings().get("webhook_url", "")).strip()
    if not webhook:
        return
    payload = {
        "event": "model_detector_alert",
        "upstream": upstream["name"],
        "verdict": result["verdict"],
        "likely_channel": result["likely_channel"],
        "confidence": result["confidence"],
        "summary": result["summary"],
        "run_id": run_id,
    }
    try:
        async with httpx.AsyncClient(timeout=8, follow_redirects=False) as client:
            await client.post(webhook, json=payload)
    except httpx.HTTPError:
        pass


def aggregate_model_results(model_results: list[dict[str, Any]]) -> dict[str, Any]:
    if not model_results:
        return {
            "verdict": "inconclusive",
            "likely_channel": "unknown",
            "confidence": 0.0,
            "summary": "未选择可检测的 GPT、Claude 或 gpt-image-2 模型",
        }
    order = {"suspected_substitution": 4, "probable_alternate_channel": 3, "confirmed_direct": 2, "inconclusive": 1}
    strongest = max(model_results, key=lambda item: (order.get(item["verdict"], 0), item.get("confidence", 0.0)))
    penetrated = sum(1 for item in model_results if item.get("success_probes", 0) > 0)
    return {
        "verdict": strongest["verdict"],
        "likely_channel": strongest["likely_channel"],
        "confidence": strongest["confidence"],
        "summary": (
            f"{strongest['model']}：{strongest['summary']}；"
            f"本轮共检测 {len(model_results)} 个模型，{penetrated} 个至少一个探针收到响应"
        ),
    }


def model_declaration_warnings(model_results: list[dict[str, Any]]) -> list[dict[str, Any]]:
    warnings: list[dict[str, Any]] = []
    for result in model_results:
        expected_model = str(result.get("model") or "").removeprefix("models/").lower()
        declared: set[str] = set()
        for evidence in result.get("evidence", []):
            if evidence.category != "response_model":
                continue
            value = str(evidence.detail.get("response_model") or "").removeprefix("models/").lower()
            if value:
                declared.add(value)
        different = sorted(value for value in declared if value != expected_model)
        if different:
            warnings.append(
                {
                    "expected_model": expected_model,
                    "declared_models": different,
                    "severity": "warning",
                    "note": "响应声明模型与请求模型不同，但该字段可由中转改写，因此不会单独触发自动禁用",
                }
            )
    return warnings


def evaluate_compliance(
    upstream: dict[str, Any], result: dict[str, Any], model_results: list[dict[str, Any]]
) -> dict[str, Any]:
    expected = str(upstream.get("expected_channel") or upstream.get("claimed_channel") or "unknown")
    likely = str(result.get("likely_channel") or "unknown")
    confidence = float(result.get("confidence") or 0.0)
    threshold = float(db.settings().get("auto_disable_min_confidence", 0.95))
    warnings = model_declaration_warnings(model_results)
    incompatible = {
        "openai_official": {
            "azure_openai", "codex_subscription_relay", "claude_subscription_relay",
            "claude_compatibility_relay", "gemini_compatibility_relay", "antigravity_subscription_relay",
            "aws_bedrock", "vertex_ai", "heterogeneous_backend_pool",
        },
        "anthropic_official": {
            "aws_bedrock", "vertex_ai", "claude_subscription_relay", "claude_compatibility_relay",
            "codex_subscription_relay", "heterogeneous_backend_pool",
        },
        "gemini_developer_api": {
            "vertex_ai", "antigravity_subscription_relay", "gemini_compatibility_relay",
            "codex_subscription_relay", "heterogeneous_backend_pool",
        },
        "azure_openai": {"openai_official", "codex_subscription_relay", "aws_bedrock", "vertex_ai"},
        "aws_bedrock": {"anthropic_official", "vertex_ai", "claude_subscription_relay"},
        "vertex_ai": {"gemini_developer_api", "aws_bedrock", "antigravity_subscription_relay"},
        "codex_subscription_relay": {"openai_official", "azure_openai"},
        "claude_subscription_relay": {"anthropic_official", "aws_bedrock", "vertex_ai"},
        "antigravity_subscription_relay": {"gemini_developer_api", "vertex_ai"},
    }
    if expected in {"", "unknown", "relay_or_custom"}:
        status = "not_configured"
        reason = "尚未为该 New API 渠道设置期望来源，只生成溯源报告，不执行自动禁用"
    elif not model_results or sum(int(item.get("success_probes") or 0) for item in model_results) == 0:
        status = "inconclusive"
        reason = "没有有效模型探针成功穿透，禁止将网络、限流、余额或上游故障当作来源不一致"
    elif likely == expected and confidence >= 0.65:
        status = "match"
        reason = "检测来源与期望来源一致"
    elif likely in incompatible.get(expected, set()) and confidence >= threshold and result.get("verdict") in {
        "suspected_substitution", "probable_alternate_channel", "confirmed_direct"
    }:
        status = "mismatch_confirmed"
        reason = f"期望 {expected}，但高置信度证据指向 {likely}（{confidence:.1%}）"
    else:
        status = "inconclusive"
        reason = "现有证据不足以安全执行自动禁用；继续保留渠道并等待后续检测"
    return {
        "status": status,
        "reason": reason,
        "expected_channel": expected,
        "likely_channel": likely,
        "confidence": confidence,
        "threshold": threshold,
        "model_declaration_warnings": warnings,
    }


def notification_settings() -> dict[str, Any]:
    settings = db.settings()
    encrypted = str(settings.get("smtp_password_encrypted") or "")
    settings["smtp_password"] = decrypt_secret(encrypted) if encrypted else ""
    return settings


async def enforce_and_notify(
    upstream: dict[str, Any], result: dict[str, Any], compliance: dict[str, Any], run_id: int
) -> tuple[str, str]:
    if compliance["status"] != "mismatch_confirmed":
        return "none", ""
    auto_action = "alert_only"
    auto_detail = "自动禁用未启用"
    settings = db.settings()
    channel_id = upstream.get("new_api_channel_id")
    should_disable = (
        bool(settings.get("auto_disable_enabled", False))
        and bool(upstream.get("auto_disable_on_mismatch", False))
        and channel_id is not None
        and new_api.configured
    )
    if should_disable:
        reason = f"模型溯源检测报告 #{run_id}：{compliance['reason']}"
        try:
            response = await new_api.disable_channel(int(channel_id), reason, run_id)
            auto_action = "new_api_channel_disabled"
            auto_detail = json.dumps(response, ensure_ascii=False, separators=(",", ":"))
            db.execute(
                "UPDATE upstreams SET enabled=0,new_api_channel_status=3,updated_at=? WHERE new_api_channel_id=?",
                (utc_now(), int(channel_id)),
            )
            action_status = "completed"
        except NewAPIIntegrationError as exc:
            auto_action = "disable_failed"
            auto_detail = str(exc)[:500]
            action_status = "failed"
        db.execute(
            "INSERT INTO channel_actions(run_id,upstream_id,new_api_channel_id,action,status,reason,detail_json,created_at) "
            "VALUES(?,?,?,?,?,?,?,?)",
            (
                run_id,
                upstream["id"],
                int(channel_id),
                "disable",
                action_status,
                reason,
                auto_detail if auto_detail.startswith("{") else json.dumps({"detail": auto_detail}, ensure_ascii=False),
                utc_now(),
            ),
        )
    report = {
        "run_id": run_id,
        "upstream_name": upstream["name"],
        "new_api_channel_id": channel_id,
        "compliance_status": compliance["status"],
        "expected_channel": compliance["expected_channel"],
        "likely_channel": compliance["likely_channel"],
        "confidence": compliance["confidence"],
        "auto_action": auto_action,
        "summary": result.get("summary", ""),
    }

    def record(destination: str, status: str, detail: str) -> None:
        db.execute(
            "INSERT INTO notification_events(run_id,destination,status,detail,created_at) VALUES(?,?,?,?,?)",
            (run_id, destination, status, detail, utc_now()),
        )

    await send_notifications(notification_settings(), report, record)
    return auto_action, auto_detail


def _integer(value: Any) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) else 0


def capability_usage_profile(detail: dict[str, Any]) -> dict[str, Any] | None:
    usage = detail.get("usage")
    if not isinstance(usage, dict):
        return None
    billing = usage.get("billing_usage")
    billing = billing if isinstance(billing, dict) else {}
    claude_usage = billing.get("claude_usage")
    claude_usage = claude_usage if isinstance(claude_usage, dict) else {}
    prompt_details = usage.get("prompt_tokens_details")
    prompt_details = prompt_details if isinstance(prompt_details, dict) else {}
    input_tokens = _integer(usage.get("prompt_tokens")) or _integer(usage.get("input_tokens")) or _integer(claude_usage.get("input_tokens"))
    cache_creation = (
        _integer(claude_usage.get("cache_creation_input_tokens"))
        or _integer(usage.get("cache_creation_input_tokens"))
        or _integer(prompt_details.get("cache_creation_tokens"))
        or _integer(prompt_details.get("cached_creation_tokens"))
    )
    hidden_minimum_input = int(CLAUDE_HISTORY_RULE.get("hidden_minimum_input_tokens", 2500))
    hidden_minimum_cache = int(CLAUDE_HISTORY_RULE.get("hidden_minimum_cache_creation_tokens", 2000))
    lightweight_maximum_input = int(CLAUDE_HISTORY_RULE.get("lightweight_maximum_input_tokens", 500))
    lightweight_maximum_cache = int(CLAUDE_HISTORY_RULE.get("lightweight_maximum_cache_creation_tokens", 500))
    if input_tokens >= hidden_minimum_input and cache_creation >= hidden_minimum_cache:
        profile_kind = "claude_code_hidden_prompt"
    elif input_tokens <= lightweight_maximum_input and cache_creation <= lightweight_maximum_cache:
        profile_kind = "lightweight_adapter"
    else:
        profile_kind = "intermediate"
    return {
        "kind": profile_kind,
        "input_tokens": input_tokens,
        "cache_creation_tokens": cache_creation,
        "usage_source": usage.get("usage_source"),
        "billing_source": billing.get("source"),
        "usage_keys": sorted(str(key) for key in usage),
    }


def historical_route_change_evidence(current: dict[str, Any], history: list[dict[str, Any]]) -> Evidence | None:
    profiles = [item for item in [*history, current] if item]
    kinds = {str(item.get("kind")) for item in profiles}
    if not {"claude_code_hidden_prompt", "lightweight_adapter"}.issubset(kinds):
        return None
    hidden = next(item for item in profiles if item.get("kind") == "claude_code_hidden_prompt")
    lightweight = next(item for item in reversed(profiles) if item.get("kind") == "lightweight_adapter")
    ratio = round(_integer(hidden.get("input_tokens")) / max(1, _integer(lightweight.get("input_tokens"))), 1)
    return Evidence(
        "historical_capability_consistency",
        "historical_route_change",
        "strong",
        "heterogeneous_backend_pool",
        "同一 Claude 模型的固定探针出现互斥后端行为",
        {
            "rule_id": "claude_historical_route_change_v1",
            "comparison_probe": "model_capability",
            "hidden_prompt_profile": hidden,
            "lightweight_profile": lightweight,
            "input_token_ratio": ratio,
            "observed_profiles": profiles[-8:],
            "conclusion": "至少发生过后端路由池异构或供应商渠道切换；黑盒无法区分同时轮询与时间上的配置更换",
        },
    )


def historical_cliproxyapi_evidence(upstream_id: int, model: str) -> Evidence | None:
    lookback_runs = int(CLIPROXY_HISTORY_RULE.get("lookback_runs", 12))
    rows = db.rows(
        "SELECT r.id AS run_id,r.started_at,e.detail_json FROM evidence e "
        "JOIN runs r ON r.id=e.run_id "
        "WHERE r.upstream_id=? AND r.mode='active' AND r.status='completed' "
        "AND e.model=? AND e.category='observation' "
        "ORDER BY r.id DESC,e.id DESC LIMIT ?",
        (upstream_id, model, lookback_runs * 12),
    )
    for row in rows:
        try:
            detail = json.loads(row["detail_json"])
        except (json.JSONDecodeError, TypeError):
            continue
        headers = detail.get("headers")
        if not isinstance(headers, dict):
            continue
        fingerprint = cliproxyapi_header_fingerprint({str(key).lower(): str(value) for key, value in headers.items()})
        if not fingerprint:
            continue
        return Evidence(
            "historical_cpa_headers",
            "cliproxyapi_implementation",
            "strong",
            "codex_subscription_relay",
            "近期同一模型曾返回 CLIProxyAPI 专属 CPA 响应头",
            {
                "rule_id": "cliproxyapi_cpa_headers_history_v1",
                "source_run_id": row["run_id"],
                "source_started_at": row["started_at"],
                **fingerprint,
                "note": "负载均衡不会保证每轮抽中同一节点；在有限回看窗口内保留已确认的实现级指纹，不能据此声称当前每个请求都经过该节点",
            },
        )
    return None


def historical_claude_divergence_evidence(upstream_id: int, model: str) -> Evidence | None:
    lookback_runs = int(CLAUDE_HISTORY_RULE.get("lookback_runs", 12))
    row = db.row(
        "SELECT r.id AS run_id,r.started_at,e.detail_json,e.raw_sha256 FROM evidence e "
        "JOIN runs r ON r.id=e.run_id "
        "WHERE r.upstream_id=? AND r.mode='active' AND r.status='completed' "
        "AND e.model=? AND e.category='within_run_route_divergence' "
        "AND r.id IN (SELECT id FROM runs WHERE upstream_id=? AND mode='active' AND status='completed' "
        "ORDER BY id DESC LIMIT ?) "
        "ORDER BY r.id DESC,e.id DESC LIMIT 1",
        (upstream_id, model, upstream_id, lookback_runs),
    )
    if not row:
        return None
    try:
        source_detail = json.loads(row["detail_json"])
    except (json.JSONDecodeError, TypeError):
        source_detail = {}
    return Evidence(
        "historical_within_run_consistency",
        "historical_within_run_route_divergence",
        "strong",
        "heterogeneous_backend_pool",
        "近期同一模型曾在单轮中命中互斥后端路径",
        {
            "rule_id": "historical_within_run_route_divergence_v1",
            "source_run_id": row["run_id"],
            "source_started_at": row["started_at"],
            "source_rule_id": source_detail.get("rule_id"),
            "source_conclusion": source_detail.get("conclusion"),
            "lookback_runs": lookback_runs,
            "note": "负载均衡可能让当前轮只抽到轻量节点；在有限回看窗口内保留已确认的同轮互斥路径，不能据此声称每个请求都经过相同后端",
        },
        row["raw_sha256"],
    )


def apply_historical_route_changes(upstream_id: int, model_results: list[dict[str, Any]]) -> list[dict[str, Any]]:
    confidence = float(CLAUDE_HISTORY_RULE.get("confidence", 0.93))
    lookback_runs = int(CLAUDE_HISTORY_RULE.get("lookback_runs", 12))
    for result in model_results:
        if result.get("family") == "openai" and str(result.get("model", "")).lower().startswith("gpt-5"):
            evidence_items = result.get("evidence", [])
            has_current_cliproxy = any(item.category == "cliproxyapi_implementation" for item in evidence_items)
            if not has_current_cliproxy:
                historical = historical_cliproxyapi_evidence(upstream_id, str(result["model"]))
                if historical:
                    evidence_items.append(historical)
                    terminal = classify(evidence_items, "openai_official")
                    result.update(
                        {
                            "verdict": terminal["verdict"],
                            "likely_channel": terminal["likely_channel"],
                            "confidence": terminal["confidence"],
                            "summary": terminal["summary"] + "；CPA 指纹来自近期同模型历史节点，本轮可能被负载均衡到另一出口",
                        }
                    )
                    chain = result.get("chain") or {"layers": []}
                    layers = chain.setdefault("layers", [])
                    if not any(item.get("kind") == "cliproxyapi" for item in layers):
                        terminal_index = next(
                            (index for index, item in enumerate(layers) if item.get("position") == "terminal"),
                            len(layers),
                        )
                        layers.insert(
                            terminal_index,
                            {
                                "position": "intermediate",
                                "kind": "cliproxyapi",
                                "label": "CLIProxyAPI / CPA 执行层（近期历史确认）",
                                "confidence": 0.99,
                                "status": "confirmed_recent",
                                "note": "本轮未必抽中同一节点；来源为有限回看窗口内同一模型的 CPA 专属响应头",
                            },
                        )
                    chain["minimum_confirmed_hops"] = max(3, int(chain.get("minimum_confirmed_hops") or 0))
                    chain["observed_logical_layers"] = len(layers)
                    result["chain"] = chain
        if result.get("family") != "anthropic":
            continue
        evidence_items = result.get("evidence", [])
        has_current_divergence = any(item.category == "within_run_route_divergence" for item in evidence_items)
        if result.get("likely_channel") != "heterogeneous_backend_pool" and not has_current_divergence:
            historical_divergence = historical_claude_divergence_evidence(upstream_id, str(result["model"]))
            if historical_divergence:
                evidence_items.append(historical_divergence)
                current_summary = str(result.get("summary") or "")
                current_was_inconclusive = result.get("verdict") == "inconclusive"
                result.update(
                    {
                        "verdict": "suspected_substitution",
                        "likely_channel": "heterogeneous_backend_pool",
                        "confidence": max(0.94, float(result.get("confidence") or 0.0)),
                        "summary": (
                            "近期同一模型已在单轮固定探针中确认互斥后端路径；本轮可能只抽到轻量节点，不能据此恢复为稳定、单一的官方直连"
                            if current_was_inconclusive
                            else current_summary + "；近期同模型还确认过互斥后端路径，整体应判定为异构池而不是单一渠道"
                        ),
                    }
                )
                chain = result.get("chain") or {"layers": []}
                layers = chain.setdefault("layers", [])
                terminal = next((item for item in layers if item.get("position") == "terminal"), None)
                historical_terminal = {
                    "position": "terminal",
                    "kind": "heterogeneous_backend_pool",
                    "label": "异构后端池或渠道切换（近期确认）",
                    "confidence": float(result["confidence"]),
                    "status": "confirmed_recent",
                    "note": "来源为有限回看窗口内同一模型的单轮互斥路径证据；当前轮可能落到另一节点",
                }
                if terminal:
                    terminal.update(historical_terminal)
                else:
                    layers.append(historical_terminal)
                chain["minimum_confirmed_hops"] = max(2, int(chain.get("minimum_confirmed_hops") or 0))
                chain["observed_logical_layers"] = len(layers)
                result["chain"] = chain
        current_profile: dict[str, Any] | None = None
        for item in evidence_items:
            if item.probe == "model_capability" and item.category == "token_accounting":
                current_profile = capability_usage_profile(item.detail)
                break
        if not current_profile:
            continue
        rows = db.rows(
            "SELECT r.id AS run_id,r.started_at,e.detail_json FROM evidence e "
            "JOIN runs r ON r.id=e.run_id "
            "WHERE r.upstream_id=? AND r.mode='active' AND r.status='completed' "
            "AND e.model=? AND e.probe='model_capability' AND e.category='token_accounting' "
            "ORDER BY r.id DESC LIMIT ?",
            (upstream_id, result["model"], lookback_runs),
        )
        history: list[dict[str, Any]] = []
        for row in reversed(rows):
            try:
                profile = capability_usage_profile(json.loads(row["detail_json"]))
            except (json.JSONDecodeError, TypeError):
                profile = None
            if profile:
                profile["run_id"] = row["run_id"]
                profile["started_at"] = row["started_at"]
                history.append(profile)
        current_profile["run_id"] = "current"
        current_profile["started_at"] = utc_now()
        evidence = historical_route_change_evidence(current_profile, history)
        if not evidence:
            continue
        result["evidence"].append(evidence)
        layer = {
            "position": "intermediate",
            "kind": "heterogeneous_backend_pool",
            "label": "后端路由池异构或渠道发生切换",
            "confidence": confidence,
            "status": "confirmed_change",
            "note": "确认行为发生过切换，但无法仅凭黑盒区分并行轮询池和供应商配置更换",
        }
        chain = result.get("chain") or {"layers": []}
        layers = chain.setdefault("layers", [])
        terminal_index = next((index for index, item in enumerate(layers) if item.get("position") == "terminal"), len(layers))
        layers.insert(terminal_index, layer)
        chain["observed_logical_layers"] = len(layers)
        result["chain"] = chain
        if result.get("verdict") == "inconclusive":
            result.update(
                {
                    "verdict": "probable_alternate_channel",
                    "likely_channel": "heterogeneous_backend_pool",
                    "confidence": confidence,
                    "summary": "历史同一工具探针在 Claude Code 隐藏提示路径与轻量适配路径之间切换，确认后端路由不稳定；当前无法归因于单一官方渠道",
                }
            )
        else:
            result["summary"] += "；历史行为同时显示后端路由池异构或渠道切换"
    return model_results


async def save_model_results(run_id: int, model_results: list[dict[str, Any]]) -> None:
    with db.connect() as connection:
        for item in model_results:
            connection.execute(
                "INSERT INTO model_results(run_id,model,family,protocol,endpoint,status,verdict,likely_channel,confidence,summary,"
                "success_probes,planned_probes,chain_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                (
                    run_id,
                    item["model"],
                    item["family"],
                    item["protocol"],
                    item.get("endpoint"),
                    "completed",
                    item["verdict"],
                    item["likely_channel"],
                    item["confidence"],
                    item["summary"],
                    item.get("success_probes", 0),
                    item.get("planned_probes", 0),
                    json.dumps(item["chain"], ensure_ascii=False, separators=(",", ":")),
                    utc_now(),
                ),
            )
    for item in model_results:
        await save_evidence(run_id, item.get("evidence", []), item["model"])


async def execute_upstream(upstream: dict[str, Any], mode: str, trigger: str) -> int:
    started = utc_now()
    run_id = db.execute(
        "INSERT INTO runs(upstream_id,trigger,mode,status,expected_channel,rule_version,started_at) VALUES(?,?,?,?,?,?,?)",
        (
            upstream["id"],
            trigger,
            mode,
            "running",
            str(upstream.get("expected_channel") or upstream.get("claimed_channel") or "unknown"),
            RULE_VERSION,
            started,
        ),
    )
    try:
        baseline = dict(upstream)
        baseline["api_style"] = "openai"
        result, evidence = await engine.run(baseline, "safe")
        comparison = compare_with_reference(upstream, evidence)
        if comparison:
            evidence.append(comparison)
        await save_evidence(run_id, evidence)
        model_results: list[dict[str, Any]] = []
        if mode == "active":
            model_results = await engine.run_models(upstream, routes_for_upstream(upstream))
            model_results = apply_historical_route_changes(upstream["id"], model_results)
            await save_model_results(run_id, model_results)
            result = aggregate_model_results(model_results)
            compliance = evaluate_compliance(upstream, result, model_results)
        else:
            result = {
                "verdict": "inconclusive",
                "likely_channel": result.get("likely_channel", "unknown"),
                "confidence": 0.0,
                "summary": "仅完成模型列表与入口协议巡检；未调用有效模型，不能据此判断终端上游真伪",
            }
            compliance = {
                "status": "not_evaluated",
                "reason": "入口巡检不执行真实性合规判定",
                "expected_channel": str(upstream.get("expected_channel") or "unknown"),
                "likely_channel": result["likely_channel"],
                "confidence": 0.0,
            }
        db.execute(
            "UPDATE runs SET status='completed',verdict=?,likely_channel=?,confidence=?,summary=?,compliance_status=?,"
            "compliance_detail_json=?,expected_channel=?,finished_at=? WHERE id=?",
            (
                result["verdict"],
                result["likely_channel"],
                result["confidence"],
                result["summary"],
                compliance["status"],
                json.dumps(compliance, ensure_ascii=False, separators=(",", ":")),
                compliance["expected_channel"],
                utc_now(),
                run_id,
            ),
        )
        auto_action, auto_detail = await enforce_and_notify(upstream, result, compliance, run_id)
        db.execute(
            "UPDATE runs SET auto_action=?,auto_action_detail=? WHERE id=?",
            (auto_action, auto_detail, run_id),
        )
    except Exception as exc:
        db.execute(
            "UPDATE runs SET status='failed',summary=?,finished_at=? WHERE id=?",
            (f"{type(exc).__name__}: {str(exc)[:500]}", utc_now(), run_id),
        )
    return run_id


async def execute_batch(upstream_id: int | None, mode: str, trigger: str) -> list[int]:
    if run_lock.locked():
        raise HTTPException(status_code=409, detail="a detector batch is already running")
    async with run_lock:
        if upstream_id is None:
            upstreams = db.rows("SELECT * FROM upstreams WHERE enabled=1 ORDER BY role DESC,id")
        else:
            upstreams = [load_upstream(upstream_id)]
        ids: list[int] = []
        for upstream in upstreams:
            ids.append(await execute_upstream(upstream, mode, trigger))
        return ids


async def scheduler_loop() -> None:
    await asyncio.sleep(5)
    while not scheduler_stop.is_set():
        settings = db.settings()
        enabled = bool(settings.get("scheduler_enabled", False))
        interval = max(10, int(settings.get("interval_minutes", 15)))
        if enabled:
            try:
                if new_api.configured:
                    await sync_new_api_channels()
                await execute_batch(None, str(settings.get("scheduled_mode", "safe")), "schedule")
            except Exception:
                pass
        try:
            await asyncio.wait_for(scheduler_stop.wait(), timeout=interval * 60 if enabled else 60)
        except asyncio.TimeoutError:
            continue


@asynccontextmanager
async def lifespan(_: FastAPI):
    scheduler_stop.clear()
    task = asyncio.create_task(scheduler_loop())
    yield
    scheduler_stop.set()
    task.cancel()
    await asyncio.gather(task, return_exceptions=True)


app = FastAPI(title="Model Provenance Detector", docs_url=None, redoc_url=None, lifespan=lifespan)


@app.get("/healthz")
def health() -> dict[str, Any]:
    return {
        "ok": True,
        "rule_version": RULE_VERSION,
        "scheduler_enabled": bool(db.settings().get("scheduler_enabled", False)),
        "new_api_integration_configured": new_api.configured,
    }


@app.get("/detector/")
def detector_page() -> FileResponse:
    return FileResponse(ROOT / "ui" / "index.html")


@app.post("/detector/api/login")
def login(value: LoginInput, request: Request, response: Response) -> dict[str, bool]:
    if not verify_admin_token(value.token):
        raise HTTPException(status_code=401, detail="invalid detector admin token")
    response.set_cookie(
        "detector_session",
        make_session(),
        httponly=True,
        secure=request.url.scheme == "https",
        samesite="strict",
        max_age=43200,
        path="/detector",
    )
    return {"ok": True}


@app.post("/detector/api/logout")
def logout(response: Response) -> dict[str, bool]:
    response.delete_cookie("detector_session", path="/detector")
    return {"ok": True}


@app.get("/detector/api/state", dependencies=[Depends(require_admin)])
def state() -> dict[str, Any]:
    upstreams = [public_upstream(row) for row in db.rows("SELECT * FROM upstreams ORDER BY role DESC,id")]
    runs = db.rows(
        "SELECT r.*,u.name AS upstream_name FROM runs r JOIN upstreams u ON u.id=r.upstream_id "
        "ORDER BY r.started_at DESC LIMIT 100"
    )
    return {
        "upstreams": upstreams,
        "runs": runs,
        "settings": public_settings(),
        "rule_version": RULE_VERSION,
        "new_api": {
            "configured": new_api.configured,
            "last_sync_at": db.settings().get("last_new_api_sync_at", ""),
        },
    }


@app.post("/detector/api/new-api/sync", dependencies=[Depends(require_admin)])
async def sync_new_api() -> dict[str, Any]:
    if not new_api.configured:
        raise HTTPException(status_code=503, detail="New API integration is not configured")
    try:
        return await sync_new_api_channels()
    except NewAPIIntegrationError as exc:
        raise HTTPException(status_code=502, detail=str(exc)) from exc


@app.put("/detector/api/upstreams/{upstream_id}/policy", dependencies=[Depends(require_admin)])
def update_new_api_channel_policy(upstream_id: int, value: NewAPIChannelPolicyInput) -> dict[str, Any]:
    upstream = load_upstream(upstream_id)
    if value.expected_channel not in KNOWN_EXPECTED_CHANNELS:
        raise HTTPException(status_code=400, detail="unsupported expected_channel")
    db.execute(
        "UPDATE upstreams SET expected_channel=?,claimed_channel=?,auto_disable_on_mismatch=?,enabled=?,updated_at=? WHERE id=?",
        (
            value.expected_channel,
            value.expected_channel,
            int(value.auto_disable_on_mismatch),
            int(value.enabled),
            utc_now(),
            upstream_id,
        ),
    )
    if upstream.get("new_api_channel_id") is not None:
        db.execute(
            "UPDATE upstreams SET expected_channel=?,claimed_channel=?,auto_disable_on_mismatch=?,updated_at=? "
            "WHERE new_api_channel_id=?",
            (
                value.expected_channel,
                value.expected_channel,
                int(value.auto_disable_on_mismatch),
                utc_now(),
                upstream["new_api_channel_id"],
            ),
        )
    return public_upstream(load_upstream(upstream_id))


@app.post("/detector/api/discover", dependencies=[Depends(require_admin)])
async def discover_upstream_models(value: DiscoveryInput) -> dict[str, Any]:
    base_url = str(value.base_url).rstrip("/")
    try:
        validate_public_api_url(base_url)
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
    result = await discover_models(base_url, value.api_key)
    result["base_url"] = base_url
    result["supported_families"] = ["openai", "anthropic", "google"]
    return result


@app.post("/detector/api/upstreams/{upstream_id}/discover", dependencies=[Depends(require_admin)])
async def rediscover_saved_upstream_models(upstream_id: int) -> dict[str, Any]:
    upstream = load_upstream(upstream_id)
    base_url = str(upstream["base_url"]).rstrip("/")
    result = await discover_models(base_url, decrypt_secret(upstream["api_key_encrypted"]))
    result["base_url"] = base_url
    result["supported_families"] = ["openai", "anthropic", "google"]
    return result


@app.post("/detector/api/upstreams", dependencies=[Depends(require_admin)])
def create_upstream(value: UpstreamInput) -> dict[str, Any]:
    if not value.api_key:
        raise HTTPException(status_code=400, detail="api_key is required for a new upstream")
    try:
        validate_public_api_url(str(value.base_url))
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
    routes = normalized_submitted_routes(value.model_routes)
    models = [item["model"] for item in routes] or value.models
    if not models:
        raise HTTPException(status_code=400, detail="select at least one discovered model")
    if value.reference_upstream_id is not None:
        reference = load_upstream(value.reference_upstream_id)
        if reference["role"] != "reference":
            raise HTTPException(status_code=400, detail="reference_upstream_id must point to a reference upstream")
    now = utc_now()
    expected_channel = value.expected_channel if value.expected_channel != "unknown" else value.claimed_channel
    if expected_channel not in KNOWN_EXPECTED_CHANNELS:
        raise HTTPException(status_code=400, detail="unsupported expected_channel")
    upstream_id = db.execute(
        "INSERT INTO upstreams(name,base_url,api_style,claimed_channel,api_key_encrypted,api_key_masked,models_json,model_routes_json,"
        "discovery_json,role,reference_upstream_id,allow_paid_probes,enabled,expected_channel,expected_models_json,"
        "auto_disable_on_mismatch,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
        (
            value.name,
            str(value.base_url).rstrip("/"),
            "auto",
            expected_channel,
            encrypt_secret(value.api_key),
            mask_secret(value.api_key),
            json.dumps(models, ensure_ascii=False),
            json.dumps(routes, ensure_ascii=False),
            json.dumps(value.discovery, ensure_ascii=False),
            value.role,
            value.reference_upstream_id,
            int(value.allow_paid_probes),
            int(value.enabled),
            expected_channel,
            json.dumps(models, ensure_ascii=False),
            int(value.auto_disable_on_mismatch),
            now,
            now,
        ),
    )
    return public_upstream(load_upstream(upstream_id))


@app.put("/detector/api/upstreams/{upstream_id}", dependencies=[Depends(require_admin)])
def update_upstream(upstream_id: int, value: UpstreamInput) -> dict[str, Any]:
    current = load_upstream(upstream_id)
    if value.reference_upstream_id is not None:
        reference = load_upstream(value.reference_upstream_id)
        if reference["role"] != "reference" or reference["id"] == upstream_id:
            raise HTTPException(status_code=400, detail="invalid reference upstream")
    try:
        validate_public_api_url(str(value.base_url))
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
    routes = normalized_submitted_routes(value.model_routes)
    models = [item["model"] for item in routes] or value.models
    encrypted = current["api_key_encrypted"]
    masked = current["api_key_masked"]
    if value.api_key:
        encrypted = encrypt_secret(value.api_key)
        masked = mask_secret(value.api_key)
    expected_channel = value.expected_channel if value.expected_channel != "unknown" else value.claimed_channel
    if expected_channel not in KNOWN_EXPECTED_CHANNELS:
        raise HTTPException(status_code=400, detail="unsupported expected_channel")
    db.execute(
        "UPDATE upstreams SET name=?,base_url=?,api_style='auto',claimed_channel=?,api_key_encrypted=?,api_key_masked=?,"
        "models_json=?,model_routes_json=?,discovery_json=?,role=?,reference_upstream_id=?,allow_paid_probes=?,enabled=?,"
        "expected_channel=?,expected_models_json=?,auto_disable_on_mismatch=?,updated_at=? WHERE id=?",
        (
            value.name,
            str(value.base_url).rstrip("/"),
            expected_channel,
            encrypted,
            masked,
            json.dumps(models, ensure_ascii=False),
            json.dumps(routes, ensure_ascii=False),
            json.dumps(value.discovery, ensure_ascii=False),
            value.role,
            value.reference_upstream_id,
            int(value.allow_paid_probes),
            int(value.enabled),
            expected_channel,
            json.dumps(models, ensure_ascii=False),
            int(value.auto_disable_on_mismatch),
            utc_now(),
            upstream_id,
        ),
    )
    return public_upstream(load_upstream(upstream_id))


@app.delete("/detector/api/upstreams/{upstream_id}", dependencies=[Depends(require_admin)])
def delete_upstream(upstream_id: int) -> dict[str, bool]:
    load_upstream(upstream_id)
    db.execute("DELETE FROM upstreams WHERE id=?", (upstream_id,))
    return {"ok": True}


@app.post("/detector/api/run", dependencies=[Depends(require_admin)])
async def run_detector(value: RunInput) -> dict[str, Any]:
    if new_api.configured:
        try:
            await sync_new_api_channels()
        except NewAPIIntegrationError as exc:
            raise HTTPException(status_code=502, detail=f"New API channel sync failed: {exc}") from exc
    run_ids = await execute_batch(value.upstream_id, value.mode, "manual")
    return {"run_ids": run_ids}


@app.get("/detector/api/runs/{run_id}", dependencies=[Depends(require_admin)])
def run_detail(run_id: int) -> dict[str, Any]:
    run = db.row(
        "SELECT r.*,u.name AS upstream_name,u.base_url,u.api_style,u.claimed_channel "
        "FROM runs r JOIN upstreams u ON u.id=r.upstream_id WHERE r.id=?",
        (run_id,),
    )
    if not run:
        raise HTTPException(status_code=404, detail="run not found")
    run["compliance"] = json.loads(run.pop("compliance_detail_json") or "{}")
    evidence = db.rows("SELECT * FROM evidence WHERE run_id=? ORDER BY model IS NOT NULL,model,id", (run_id,))
    for item in evidence:
        item["detail"] = json.loads(item.pop("detail_json"))
    model_results = db.rows("SELECT * FROM model_results WHERE run_id=? ORDER BY id", (run_id,))
    for item in model_results:
        item["chain"] = json.loads(item.pop("chain_json"))
    actions = db.rows("SELECT * FROM channel_actions WHERE run_id=? ORDER BY id", (run_id,))
    for item in actions:
        item["detail"] = json.loads(item.pop("detail_json") or "{}")
    notifications = db.rows("SELECT * FROM notification_events WHERE run_id=? ORDER BY id", (run_id,))
    return {
        "run": run,
        "model_results": model_results,
        "evidence": evidence,
        "actions": actions,
        "notifications": notifications,
    }


@app.put("/detector/api/settings", dependencies=[Depends(require_admin)])
def update_settings(value: SettingsInput) -> dict[str, Any]:
    db.set_setting("scheduler_enabled", value.scheduler_enabled)
    db.set_setting("interval_minutes", value.interval_minutes)
    db.set_setting("scheduled_mode", value.scheduled_mode)
    db.set_setting("webhook_url", value.webhook_url)
    db.set_setting("webhook_type", value.webhook_type)
    db.set_setting("email_enabled", value.email_enabled)
    db.set_setting("smtp_host", value.smtp_host)
    db.set_setting("smtp_port", value.smtp_port)
    db.set_setting("smtp_username", value.smtp_username)
    if value.smtp_password:
        db.set_setting("smtp_password_encrypted", encrypt_secret(value.smtp_password))
    db.set_setting("smtp_from", value.smtp_from)
    db.set_setting("smtp_starttls", value.smtp_starttls)
    db.set_setting("smtp_ssl", value.smtp_ssl)
    db.set_setting("alert_email_to", value.alert_email_to)
    db.set_setting("auto_disable_enabled", value.auto_disable_enabled)
    db.set_setting("auto_disable_min_confidence", value.auto_disable_min_confidence)
    return public_settings()
