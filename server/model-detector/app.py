import asyncio
import json
import os
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any

import httpx
from fastapi import Cookie, Depends, FastAPI, HTTPException, Request, Response
from fastapi.responses import FileResponse
from pydantic import BaseModel, Field, HttpUrl, field_validator

from database import Database, utc_now
from discovery import discover_models, route_for_model, validate_public_api_url
from engine import DetectorEngine, Evidence, RULE_VERSION
from security import encrypt_secret, make_session, mask_secret, validate_runtime_secrets, verify_admin_token, verify_session


ROOT = Path(__file__).resolve().parent
validate_runtime_secrets()
db = Database()
engine = DetectorEngine(float(os.environ.get("DETECTOR_TIMEOUT_SECONDS", "30")))
run_lock = asyncio.Lock()
scheduler_stop = asyncio.Event()


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
    reference_upstream_id: int | None = None
    allow_paid_probes: bool = False
    enabled: bool = True

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
    interval_minutes: int = Field(ge=10, le=1440)
    scheduled_mode: str = "safe"
    webhook_url: str = ""

    @field_validator("scheduled_mode")
    @classmethod
    def validate_scheduled_mode(cls, value: str) -> str:
        if value not in {"safe", "active"}:
            raise ValueError("scheduled_mode must be safe or active")
        return value


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
    for field in ("allow_paid_probes", "enabled"):
        result[field] = bool(result[field])
    return result


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
            "summary": "未选择可检测的 OpenAI、Anthropic 或 Google 文本模型",
        }
    order = {"suspected_substitution": 4, "probable_alternate_channel": 3, "confirmed_direct": 2, "inconclusive": 1}
    strongest = max(model_results, key=lambda item: (order.get(item["verdict"], 0), item.get("confidence", 0.0)))
    penetrated = sum(1 for item in model_results if item.get("success_probes", 0) > 0)
    return {
        "verdict": strongest["verdict"],
        "likely_channel": strongest["likely_channel"],
        "confidence": strongest["confidence"],
        "summary": f"已对 {len(model_results)} 个模型执行低 Token 穿透探针，{penetrated} 个模型至少一次成功到达有效响应；详情按模型查看",
    }


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
        "INSERT INTO runs(upstream_id,trigger,mode,status,rule_version,started_at) VALUES(?,?,?,?,?,?)",
        (upstream["id"], trigger, mode, "running", RULE_VERSION, started),
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
            await save_model_results(run_id, model_results)
            result = aggregate_model_results(model_results)
        else:
            result = {
                "verdict": "inconclusive",
                "likely_channel": result.get("likely_channel", "unknown"),
                "confidence": 0.0,
                "summary": "仅完成模型列表与入口协议巡检；未调用有效模型，不能据此判断终端上游真伪",
            }
        db.execute(
            "UPDATE runs SET status='completed',verdict=?,likely_channel=?,confidence=?,summary=?,finished_at=? WHERE id=?",
            (result["verdict"], result["likely_channel"], result["confidence"], result["summary"], utc_now(), run_id),
        )
        await notify_if_needed(upstream, result, run_id)
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
        interval = max(10, int(settings.get("interval_minutes", 15)))
        try:
            await execute_batch(None, str(settings.get("scheduled_mode", "safe")), "schedule")
        except Exception:
            pass
        try:
            await asyncio.wait_for(scheduler_stop.wait(), timeout=interval * 60)
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
    return {"ok": True, "rule_version": RULE_VERSION}


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
    return {"upstreams": upstreams, "runs": runs, "settings": db.settings(), "rule_version": RULE_VERSION}


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
    upstream_id = db.execute(
        "INSERT INTO upstreams(name,base_url,api_style,claimed_channel,api_key_encrypted,api_key_masked,models_json,model_routes_json,"
        "discovery_json,role,reference_upstream_id,allow_paid_probes,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
        (
            value.name,
            str(value.base_url).rstrip("/"),
            "auto",
            "unknown",
            encrypt_secret(value.api_key),
            mask_secret(value.api_key),
            json.dumps(models, ensure_ascii=False),
            json.dumps(routes, ensure_ascii=False),
            json.dumps(value.discovery, ensure_ascii=False),
            value.role,
            value.reference_upstream_id,
            int(value.allow_paid_probes),
            int(value.enabled),
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
    db.execute(
        "UPDATE upstreams SET name=?,base_url=?,api_style='auto',claimed_channel='unknown',api_key_encrypted=?,api_key_masked=?,"
        "models_json=?,model_routes_json=?,discovery_json=?,role=?,reference_upstream_id=?,allow_paid_probes=?,enabled=?,updated_at=? WHERE id=?",
        (
            value.name,
            str(value.base_url).rstrip("/"),
            encrypted,
            masked,
            json.dumps(models, ensure_ascii=False),
            json.dumps(routes, ensure_ascii=False),
            json.dumps(value.discovery, ensure_ascii=False),
            value.role,
            value.reference_upstream_id,
            int(value.allow_paid_probes),
            int(value.enabled),
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
    evidence = db.rows("SELECT * FROM evidence WHERE run_id=? ORDER BY model IS NOT NULL,model,id", (run_id,))
    for item in evidence:
        item["detail"] = json.loads(item.pop("detail_json"))
    model_results = db.rows("SELECT * FROM model_results WHERE run_id=? ORDER BY id", (run_id,))
    for item in model_results:
        item["chain"] = json.loads(item.pop("chain_json"))
    return {"run": run, "model_results": model_results, "evidence": evidence}


@app.put("/detector/api/settings", dependencies=[Depends(require_admin)])
def update_settings(value: SettingsInput) -> dict[str, Any]:
    db.set_setting("interval_minutes", value.interval_minutes)
    db.set_setting("scheduled_mode", value.scheduled_mode)
    db.set_setting("webhook_url", value.webhook_url)
    return db.settings()
