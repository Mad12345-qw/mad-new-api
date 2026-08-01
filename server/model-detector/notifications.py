import asyncio
import html
import json
import smtplib
import ssl
from email.message import EmailMessage
from typing import Any, Callable

import httpx


def _send_email_sync(settings: dict[str, Any], subject: str, text_body: str, html_body: str) -> None:
    host = str(settings.get("smtp_host") or "").strip()
    port = int(settings.get("smtp_port") or 587)
    username = str(settings.get("smtp_username") or "").strip()
    password = str(settings.get("smtp_password") or "")
    sender = str(settings.get("smtp_from") or username).strip()
    recipients = [item.strip() for item in str(settings.get("alert_email_to") or "").split(",") if item.strip()]
    if not host or not sender or not recipients:
        raise ValueError("SMTP host, sender, and recipient are required")
    message = EmailMessage()
    message["Subject"] = subject
    message["From"] = sender
    message["To"] = ", ".join(recipients)
    message.set_content(text_body)
    message.add_alternative(html_body, subtype="html")
    context = ssl.create_default_context()
    if bool(settings.get("smtp_ssl", False)):
        with smtplib.SMTP_SSL(host, port, timeout=15, context=context) as client:
            if username:
                client.login(username, password)
            client.send_message(message)
        return
    with smtplib.SMTP(host, port, timeout=15) as client:
        client.ehlo()
        if bool(settings.get("smtp_starttls", True)):
            client.starttls(context=context)
            client.ehlo()
        if username:
            client.login(username, password)
        client.send_message(message)


async def send_notifications(
    settings: dict[str, Any],
    report: dict[str, Any],
    record: Callable[[str, str, str], None],
) -> None:
    subject = (
        f"[模型检测告警] New API 渠道 #{report.get('new_api_channel_id', '-')} "
        f"{report.get('upstream_name', '')}：{report.get('compliance_status', '')}"
    )
    text_body = "\n".join(
        [
            subject,
            f"检测报告：#{report.get('run_id')}",
            f"期望渠道：{report.get('expected_channel')}",
            f"检测渠道：{report.get('likely_channel')}",
            f"置信度：{round(float(report.get('confidence') or 0) * 100)}%",
            f"自动动作：{report.get('auto_action', 'none')}",
            f"结论：{report.get('summary', '')}",
        ]
    )
    html_body = (
        "<html><body style='font-family:system-ui,sans-serif'>"
        f"<h2>{html.escape(subject)}</h2>"
        f"<p><b>检测报告：</b>#{html.escape(str(report.get('run_id')))}</p>"
        f"<p><b>期望渠道：</b>{html.escape(str(report.get('expected_channel')))}</p>"
        f"<p><b>检测渠道：</b>{html.escape(str(report.get('likely_channel')))}</p>"
        f"<p><b>置信度：</b>{round(float(report.get('confidence') or 0) * 100)}%</p>"
        f"<p><b>自动动作：</b>{html.escape(str(report.get('auto_action', 'none')))}</p>"
        f"<p><b>结论：</b>{html.escape(str(report.get('summary', '')))}</p>"
        "</body></html>"
    )
    if bool(settings.get("email_enabled", False)):
        try:
            await asyncio.to_thread(_send_email_sync, settings, subject, text_body, html_body)
            record("email", "sent", "")
        except (OSError, ValueError, smtplib.SMTPException) as exc:
            record("email", "failed", f"{type(exc).__name__}: {str(exc)[:300]}")
    webhook = str(settings.get("webhook_url") or "").strip()
    if webhook:
        webhook_type = str(settings.get("webhook_type") or "generic")
        if webhook_type == "feishu":
            payload: dict[str, Any] = {"msg_type": "text", "content": {"text": text_body}}
        elif webhook_type == "dingtalk":
            payload = {"msgtype": "text", "text": {"content": text_body}}
        else:
            payload = {"event": "model_detector_alert", "report": report}
        try:
            async with httpx.AsyncClient(timeout=10, follow_redirects=False) as client:
                response = await client.post(webhook, json=payload)
                if response.status_code < 200 or response.status_code >= 300:
                    raise httpx.HTTPStatusError(
                        f"HTTP {response.status_code}", request=response.request, response=response
                    )
            record("webhook", "sent", json.dumps({"type": webhook_type}, ensure_ascii=False))
        except httpx.HTTPError as exc:
            record("webhook", "failed", f"{type(exc).__name__}: {str(exc)[:300]}")
