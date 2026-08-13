import argparse
import concurrent.futures
import json
import statistics
import time
import urllib.error
import urllib.request


PNG = b"\x89PNG\r\n\x1a\n" + b"\0" * 4096


def post_json(base, model, response_format="url"):
    payload = json.dumps({
        "model": model,
        "prompt": "Replay fixture. No real model is called.",
        "response_format": response_format,
        "size": "2K" if model.endswith("-2k") else "1024x1024",
        "n": 1,
    }).encode()
    request = urllib.request.Request(
        base + "/v1/images/generations",
        data=payload,
        headers={"Content-Type": "application/json", "Authorization": "Bearer mock"},
        method="POST",
    )
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(request, timeout=360) as response:
            raw = response.read()
            status = response.status
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        status = exc.code
    elapsed = time.perf_counter() - started
    result = {"status": status, "bytes": len(raw), "elapsed": elapsed, "valid": False}
    try:
        item = (json.loads(raw).get("data") or [])[0]
        result["valid"] = bool(item.get("url")) if response_format == "url" else bool(item.get("b64_json"))
    except Exception:
        pass
    return result


def run_phase(name, base, model, count, concurrency, response_format="url"):
    started = time.perf_counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
        results = list(pool.map(lambda _: post_json(base, model, response_format), range(count)))
    elapsed = time.perf_counter() - started
    latencies = sorted(item["elapsed"] for item in results)
    report = {
        "phase": name,
        "model": model,
        "count": count,
        "concurrency": concurrency,
        "ok": sum(item["status"] == 200 and item["valid"] for item in results),
        "errors": sum(item["status"] != 200 or not item["valid"] for item in results),
        "wall_seconds": round(elapsed, 3),
        "requests_per_second": round(count / elapsed, 2),
        "response_bytes_total": sum(item["bytes"] for item in results),
        "latency_median": round(statistics.median(latencies), 3),
        "latency_p95": round(latencies[min(len(latencies) - 1, int(len(latencies) * 0.95))], 3),
        "latency_max": round(max(latencies), 3),
    }
    print(json.dumps(report, ensure_ascii=False), flush=True)
    return report


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default="http://127.0.0.1:19081")
    args = parser.parse_args()
    reports = [
        run_phase("image2-url", args.base, "gpt-image-2", 120, 48),
        run_phase("gemini-inline-to-url", args.base, "gemini-3.1-flash-image-preview-2k", 80, 12),
        run_phase("legacy-b64-stream", args.base, "gpt-image-2", 12, 12, "b64_json"),
    ]
    if any(item["errors"] for item in reports):
        raise SystemExit(1)


if __name__ == "__main__":
    main()
