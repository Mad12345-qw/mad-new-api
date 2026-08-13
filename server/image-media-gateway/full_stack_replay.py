#!/usr/bin/env python3
import argparse
import concurrent.futures
import json
import statistics
import time
import urllib.error
import urllib.request


def request_image(base, api_key, model):
    body = json.dumps(
        {
            "model": model,
            "prompt": "Synthetic full-stack replay. No real model is called.",
            "response_format": "url",
            "n": 1,
        }
    ).encode()
    request = urllib.request.Request(
        base + "/v1/images/generations",
        data=body,
        headers={"Authorization": "Bearer " + api_key, "Content-Type": "application/json"},
        method="POST",
    )
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(request, timeout=300) as response:
            raw = response.read()
            status = response.status
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        status = exc.code
    elapsed = time.perf_counter() - started
    valid = False
    url = ""
    try:
        item = (json.loads(raw).get("data") or [])[0]
        url = str(item.get("url") or "")
        valid = bool(url) and "b64_json" not in item
    except Exception:
        pass
    return {"status": status, "elapsed": elapsed, "valid": valid, "url": url, "bytes": len(raw)}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", required=True)
    parser.add_argument("--api-key", required=True)
    parser.add_argument("--model", default="gemini-3.1-flash-image-preview-2k")
    parser.add_argument("--count", type=int, default=24)
    parser.add_argument("--concurrency", type=int, default=8)
    args = parser.parse_args()
    started = time.perf_counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
        results = list(pool.map(lambda _: request_image(args.base, args.api_key, args.model), range(args.count)))
    elapsed = time.perf_counter() - started
    latencies = sorted(item["elapsed"] for item in results)
    report = {
        "count": args.count,
        "concurrency": args.concurrency,
        "ok": sum(item["status"] == 200 and item["valid"] for item in results),
        "errors": sum(item["status"] != 200 or not item["valid"] for item in results),
        "wall_seconds": round(elapsed, 3),
        "latency_median": round(statistics.median(latencies), 3),
        "latency_p95": round(latencies[min(len(latencies) - 1, int(len(latencies) * 0.95))], 3),
        "latency_max": round(max(latencies), 3),
        "response_bytes_total": sum(item["bytes"] for item in results),
        "sample_url": next((item["url"] for item in results if item["url"]), ""),
    }
    print(json.dumps(report, separators=(",", ":")))
    raise SystemExit(1 if report["errors"] else 0)


if __name__ == "__main__":
    main()
