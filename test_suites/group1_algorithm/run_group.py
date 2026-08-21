#!/usr/bin/env python3
"""第一组：分离性能计时与完整协议证据的 Algorithm Server 演示。"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any

GROUP_DIR = Path(__file__).resolve().parent
SUITES_DIR = GROUP_DIR.parent
PROJECT_ROOT = SUITES_DIR.parent
sys.path.insert(0, str(SUITES_DIR))

from common.fixture import generate, load_config  # noqa: E402
from common.mock_prometheus import start as start_prometheus  # noqa: E402
from common.runtime import distribution, run_id, write_json  # noqa: E402

HTTP_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def write_raw(path: Path, raw: bytes | str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    value = raw.encode() if isinstance(raw, str) else raw
    path.write_bytes(value if value.endswith(b"\n") else value + b"\n")


def link_file(source: Path, destination: Path) -> None:
    destination.parent.mkdir(parents=True, exist_ok=True)
    try:
        destination.hardlink_to(source)
    except OSError:
        shutil.copy2(source, destination)


def http_json(method: str, url: str, payload: dict[str, Any] | None = None, timeout: float = 60) -> tuple[dict[str, Any], int, bytes, bytes]:
    request_body = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode() if payload is not None else b""
    request = urllib.request.Request(url, data=request_body if payload is not None else None, method=method, headers={"Accept": "application/json", "Content-Type": "application/json"})
    started = time.perf_counter_ns()
    try:
        with HTTP_OPENER.open(request, timeout=timeout) as response:
            response_body = response.read()
    except urllib.error.HTTPError as exc:
        detail = exc.read()
        raise RuntimeError(f"{method} {url} returned HTTP {exc.code}: {detail.decode(errors='replace')}") from exc
    elapsed = time.perf_counter_ns() - started
    return json.loads(response_body), elapsed, request_body, response_body


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def docker(*args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(["docker", *args], check=check, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)


def start_algorithm(image: str, prometheus_port: int, token_file: Path, evidence_dir: Path | None = None) -> tuple[str, str]:
    if shutil.which("docker") is None:
        raise RuntimeError("缺少 docker")
    if docker("image", "inspect", image, check=False).returncode != 0:
        raise RuntimeError(f"未找到镜像 {image}，请先运行 make algorithm-image")
    port = free_port()
    name = f"ngd-ngg-test-g1-{os.getpid()}-{port}"
    arguments = [
        "run", "--detach", "--name", name,
        "--add-host", "host.docker.internal:host-gateway",
        "--publish", f"127.0.0.1:{port}:8080",
        "--volume", f"{token_file}:/run/secrets/prometheus-token:ro",
        "--env", f"PROMETHEUS_URL=http://host.docker.internal:{prometheus_port}",
        "--env", "PROMETHEUS_BEARER_TOKEN_FILE=/run/secrets/prometheus-token",
        "--env", "PROMETHEUS_REFRESH_SECONDS=1",
        "--env", "PROMETHEUS_STALE_SECONDS=120",
        "--env", "PROMETHEUS_REQUEST_TIMEOUT_SECONDS=30",
    ]
    if evidence_dir is not None:
        evidence_dir.mkdir(parents=True, exist_ok=True)
        evidence_dir.chmod(0o777)
        arguments.extend(["--volume", f"{evidence_dir}:/evidence", "--env", "ALGORITHM_WORKER_EVIDENCE_DIR=/evidence"])
    arguments.append(image)
    result = docker(*arguments)
    if not result.stdout.strip():
        raise RuntimeError("Algorithm container did not return an ID")
    return name, f"http://127.0.0.1:{port}"


def wait_api(base_url: str, timeout: float = 40) -> None:
    deadline = time.monotonic() + timeout
    last = ""
    while time.monotonic() < deadline:
        try:
            value, _, _, _ = http_json("GET", base_url + "/healthz", timeout=2)
            if value.get("status") == "ok":
                return
        except Exception as exc:
            last = str(exc)
        time.sleep(0.1)
    raise RuntimeError("Algorithm health timeout: " + last)


def cache_status(base_url: str) -> dict[str, Any]:
    value, _, _, _ = http_json("GET", base_url + "/internal/v1/cache/status", timeout=3)
    return value


def wait_cache(base_url: str, snapshot_id: str, node_count: int, timeout: float = 60) -> dict[str, Any]:
    deadline = time.monotonic() + timeout
    last: dict[str, Any] = {}
    while time.monotonic() < deadline:
        last = cache_status(base_url)
        static = last.get("nodeStatic", {})
        metrics = last.get("metrics", {})
        coverage = metrics.get("coverage", {})
        if static.get("currentSnapshotId") == snapshot_id and metrics.get("ready") and metrics.get("nodeCount") == node_count and len(coverage) == 14 and all(value == node_count for value in coverage.values()):
            return last
        time.sleep(0.1)
    raise RuntimeError("Algorithm cache timeout: " + json.dumps(last, ensure_ascii=False))


def verify(response: dict[str, Any], fixture: dict[str, Any]) -> dict[str, Any]:
    assert response.get("status") == "SUCCESS", response
    assert response.get("nodeStaticSnapshotId") == fixture["snapshotId"]
    assert response.get("degraded") is False, response.get("warnings")
    groups = response.get("candidateNodeGroups", [])
    assert len(groups) == 2, f"expected 2 core groups, got {len(groups)}"
    busy = {item["nodeUID"] for item in fixture["dynamicState"]["nodes"] if item["inUse"]}
    seen: set[str] = set()
    for rank, group in enumerate(groups, 1):
        assert group["rank"] == rank
        assert group["topologyLevel"] == "coreSwitch"
        assert len(group["nodes"]) == 1000
        scores = [node["score"] for node in group["nodes"]]
        assert scores == sorted(scores, reverse=True)
        for node in group["nodes"]:
            assert node["nodeUID"] not in busy
            assert node["nodeUID"] not in seen
            seen.add(node["nodeUID"])
    return {"candidateGroupCount": len(groups), "candidateNodeCounts": [len(item["nodes"]) for item in groups], "topologyLevels": [item["topologyLevel"] for item in groups]}


def algorithm_processing_ms(response: dict[str, Any]) -> float:
    timing = response.get("timing") or {}
    assert timing.get("unit") == "ms", timing
    value = float(timing.get("algorithmProcessingMs", -1))
    assert value >= 0, timing
    return value


def stop_services(container: str, prometheus: Any, log_path: Path | None = None) -> None:
    if container:
        logs = docker("logs", container, check=False).stdout
        if log_path is not None:
            log_path.parent.mkdir(parents=True, exist_ok=True)
            log_path.write_text(logs, encoding="utf-8")
        docker("rm", "--force", container, check=False)
    if prometheus is not None:
        prometheus.shutdown()
        prometheus.server_close()


def run_timing(root: Path, fixture: dict[str, Any], image: str, warmups: int, measured_runs: int, token_file: Path) -> dict[str, Any]:
    catalogue = json.loads((PROJECT_ROOT / "algorithm_server/go/prometheus_metrics.json").read_text(encoding="utf-8"))
    prometheus, state = start_prometheus(fixture["metrics"], catalogue, load_config()["prometheus"]["bearerToken"], None)
    container = ""
    try:
        container, base_url = start_algorithm(image, int(prometheus.server_address[1]), token_file)
        wait_api(base_url)
        state.allowed.set()
        encoded = urllib.parse.quote(fixture["snapshotId"], safe="")
        cold_request = dict(fixture["allocationRequest"])
        cold_request["requestId"] = "group1-cold-timing"
        cold_started = time.perf_counter_ns()
        ack, _, _, _ = http_json("PUT", base_url + "/internal/v1/node-static-snapshots/" + encoded, fixture["staticSnapshot"])
        assert ack["acceptedSnapshotId"] == fixture["snapshotId"]
        wait_cache(base_url, fixture["snapshotId"], 3000)
        cold_response, _, _, _ = http_json("POST", base_url + "/api/v1/allocate", cold_request)
        cold_round_trip = round((time.perf_counter_ns() - cold_started) / 1_000_000, 3)
        cold_processing = round(algorithm_processing_ms(cold_response), 3)
        cold_verified = verify(cold_response, fixture)

        prc_times: list[float] = []
        processing_times: list[float] = []
        last_response: dict[str, Any] = {}
        for index in range(warmups + measured_runs):
            request = dict(fixture["allocationRequest"])
            request["requestId"] = f"group1-warm-timing-{index + 1:04d}"
            response, elapsed, _, _ = http_json("POST", base_url + "/api/v1/allocate", request)
            verify(response, fixture)
            if index >= warmups:
                prc_times.append(elapsed / 1_000_000)
                processing_times.append(algorithm_processing_ms(response))
                last_response = response
        result = {
            "status": "PASS", "mode": "timing", "unit": "ms", "fixtureIdentity": fixture["snapshotId"],
            "excluded": ["evidence capture", "debugTrace", "intermediate serialization", "file writes"],
            "cold_cache": {"prcSendToReceiveMs": cold_round_trip, "algorithmProcessingMs": cold_processing, **cold_verified},
            "warm_cache": {
                "warmupRuns": warmups, "measuredRuns": measured_runs,
                "prcSendToReceiveDistribution": distribution(prc_times),
                "algorithmProcessingDistribution": distribution(processing_times),
                **verify(last_response, fixture),
            },
        }
        write_json(root / "timing-result.json", result)
        (root / "summary.md").write_text(
            "# 第一组 Timing Run\n\n"
            f"- Cold PRC发送到接收：`{cold_round_trip} ms`\n"
            f"- Cold Algorithm内部：`{cold_processing} ms`\n"
            f"- Warm PRC P95：`{result['warm_cache']['prcSendToReceiveDistribution']['p95Ms']} ms`\n"
            f"- Warm Algorithm P95：`{result['warm_cache']['algorithmProcessingDistribution']['p95Ms']} ms`\n"
            "- 详细证据采集、Trace和文件写入均不在计时运行中执行。\n",
            encoding="utf-8",
        )
        return {"cold": cold_response["candidateNodeGroups"], "warm": last_response["candidateNodeGroups"], "result": result}
    finally:
        stop_services(container, prometheus)


def pipeline_files(case_root: Path, worker_response: dict[str, Any]) -> None:
    stages = worker_response.get("result", {}).get("pipelineTrace", [])
    if len(stages) != 3:
        raise AssertionError(f"expected three Python pipeline stages, got {len(stages)}")
    for index, stage in enumerate(stages, 1):
        name = str(stage.get("algorithm", "unknown"))
        write_json(case_root / "04-python-pipeline" / f"{index:02d}-{name}-input.json", stage.get("input", {}))
        write_json(case_root / "04-python-pipeline" / f"{index:02d}-{name}-output.json", stage.get("output", {}))


def split_worker_evidence(raw_root: Path, cases: dict[str, Path]) -> dict[str, dict[str, Any]]:
    request_lines = (raw_root / "go-to-python-request.jsonl").read_text(encoding="utf-8").splitlines()
    response_lines = (raw_root / "python-to-go-response.jsonl").read_text(encoding="utf-8").splitlines()
    responses_by_id = {str(json.loads(line)["id"]): (line, json.loads(line)) for line in response_lines}
    results: dict[str, dict[str, Any]] = {}
    for line in request_lines:
        envelope = json.loads(line)
        request_id = str(envelope["payload"]["request"]["requestId"])
        case_name = "cold_cache" if "cold" in request_id else "warm_cache"
        if case_name not in cases:
            continue
        case_root = cases[case_name]
        response_line, response = responses_by_id[str(envelope["id"])]
        write_raw(case_root / "03-go-python-worker" / "go-to-python-request.jsonl", line)
        write_raw(case_root / "03-go-python-worker" / "python-to-go-response.jsonl", response_line)
        payload = envelope["payload"]
        write_json(case_root / "02-go-cache" / "node-static-snapshot.json", payload["staticSnapshot"])
        write_json(case_root / "02-go-cache" / "prometheus-metric-snapshot.json", payload.get("metricSnapshot"))
        write_json(case_root / "02-go-cache" / "cache-status.json", {
            "nodeStaticSnapshotId": payload["staticSnapshot"].get("snapshotId"),
            "metricSnapshotId": (payload.get("metricSnapshot") or {}).get("snapshotId"),
            "metricsDegraded": payload.get("metricsDegraded"), "warnings": payload.get("warnings"),
        })
        pipeline_files(case_root, response)
        results[case_name] = response
    return results


def run_evidence(root: Path, fixture: dict[str, Any], image: str, token_file: Path) -> dict[str, Any]:
    raw_worker = root / "infrastructure" / "worker-raw"
    prometheus_log = root / "infrastructure" / "prometheus-requests.jsonl"
    catalogue = json.loads((PROJECT_ROOT / "algorithm_server/go/prometheus_metrics.json").read_text(encoding="utf-8"))
    prometheus, state = start_prometheus(fixture["metrics"], catalogue, load_config()["prometheus"]["bearerToken"], prometheus_log)
    container = ""
    cases = {name: root / name for name in ("cold_cache", "warm_cache")}
    responses: dict[str, dict[str, Any]] = {}
    try:
        container, base_url = start_algorithm(image, int(prometheus.server_address[1]), token_file, raw_worker)
        wait_api(base_url)
        before = cache_status(base_url)
        state.allowed.set()
        encoded = urllib.parse.quote(fixture["snapshotId"], safe="")
        ack, _, put_request, put_response = http_json("PUT", base_url + "/internal/v1/node-static-snapshots/" + encoded, fixture["staticSnapshot"])
        ready = wait_cache(base_url, fixture["snapshotId"], 3000)
        assert ack["acceptedSnapshotId"] == fixture["snapshotId"]

        for case_name in ("cold_cache", "warm_cache"):
            request = dict(fixture["allocationRequest"])
            request["requestId"] = f"group1-{case_name.replace('_cache', '')}-evidence"
            request["debugTrace"] = True
            response, _, request_raw, response_raw = http_json("POST", base_url + "/api/v1/allocate", request)
            verify(response, fixture)
            case_root = cases[case_name]
            write_raw(case_root / "01-prc-go-http" / "prc-to-go-request.json", request_raw)
            go_response_path = case_root / "01-prc-go-http" / "go-to-prc-response.json"
            write_raw(go_response_path, response_raw)
            write_json(case_root / "01-prc-go-http" / "node-dynamic-state.json", {"nodes": request["nodeUsageStates"]})
            link_file(go_response_path, case_root / "05-final" / "allocation-response.json")
            responses[case_name] = response

        write_json(root / "infrastructure" / "algorithm-cache-before.json", before)
        write_json(root / "infrastructure" / "algorithm-cache-ready.json", ready)
        write_raw(root / "infrastructure" / "static-snapshot-put-request.json", put_request)
        write_raw(root / "infrastructure" / "static-snapshot-put-response.json", put_response)
        write_json(root / "infrastructure" / "prometheus-source-node-metrics.json", {"nodes": fixture["metrics"]})
    finally:
        stop_services(container, prometheus, root / "logs" / "algorithm-server.log")

    worker = split_worker_evidence(raw_worker, cases)
    shutil.rmtree(raw_worker)
    for case_name, case_root in cases.items():
        response = responses[case_name]
        python_groups = worker[case_name]["result"]["candidateNodeGroups"]
        assert python_groups == response["candidateNodeGroups"]
        (case_root / "report.md").write_text(
            f"# 第一组 Evidence Run / {case_name}\n\n"
            "查看顺序：PRC→Go HTTP、Go缓存、Go→Python、Python Pipeline、最终响应。\n\n"
            f"- 静态快照：`{response['nodeStaticSnapshotId']}`\n"
            f"- 指标快照：`{response['metricSnapshotId']}`\n"
            f"- 候选组数量：`{len(response['candidateNodeGroups'])}`\n",
            encoding="utf-8",
        )
    result = {
        "status": "PASS", "mode": "evidence", "fixtureIdentity": fixture["snapshotId"],
        "cases": {name: {"candidateGroupCount": len(value["candidateNodeGroups"])} for name, value in responses.items()},
        "note": "本次只生成原始协议和中间证据，不作为性能结果",
    }
    write_json(root / "evidence-result.json", result)
    return {"cold": responses["cold_cache"]["candidateNodeGroups"], "warm": responses["warm_cache"]["candidateNodeGroups"], "result": result}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("timing", "evidence", "all"), default="all")
    parser.add_argument("--image", default=os.getenv("ALGORITHM_IMAGE", "ngd-ngg-algorithm:v0.4.0"))
    parser.add_argument("--warmup-runs", type=int, default=5)
    parser.add_argument("--measured-runs", type=int, default=30)
    args = parser.parse_args()
    group_run = GROUP_DIR / "runs" / run_id()
    group_run.mkdir(parents=True, exist_ok=True)
    fixture = generate(load_config())
    token_file = group_run / ".prometheus-token"
    token_file.write_text(load_config()["prometheus"]["bearerToken"] + "\n", encoding="utf-8")
    # 容器以65532非root用户运行；临时文件只含Mock凭据并在finally中删除。
    token_file.chmod(0o644)
    try:
        timing = evidence = None
        if args.mode in ("timing", "all"):
            timing = run_timing(group_run / "timing-run", fixture, args.image, args.warmup_runs, args.measured_runs, token_file)
        if args.mode in ("evidence", "all"):
            evidence = run_evidence(group_run / "evidence-run", fixture, args.image, token_file)
        if timing and evidence:
            assert timing["cold"] == evidence["cold"], "cold timing/evidence candidates differ"
            assert timing["warm"] == evidence["warm"], "warm timing/evidence candidates differ"
        summary = {
            "status": "PASS", "mode": args.mode, "fixtureIdentity": fixture["snapshotId"],
            "timingRun": str(group_run / "timing-run") if timing else None,
            "evidenceRun": str(group_run / "evidence-run") if evidence else None,
            "timingAndEvidenceCoreResultsEqual": bool(timing and evidence),
        }
        write_json(group_run / "result.json", summary)
        (group_run / "report.md").write_text(
            "# 第一组：Algorithm Server\n\n"
            f"- 状态：`PASS`\n- 模式：`{args.mode}`\n- Fixture：`{fixture['snapshotId']}`\n"
            "- Timing 与 Evidence 分开执行，证据写盘不进入性能时间。\n",
            encoding="utf-8",
        )
        print(json.dumps({"status": "PASS", "result": str(group_run)}, ensure_ascii=False))
        return 0
    except BaseException as exc:
        write_json(group_run / "result.json", {"status": "FAIL", "mode": args.mode, "error": str(exc)})
        print(f"FAIL: {exc}", file=sys.stderr)
        return 1
    finally:
        token_file.unlink(missing_ok=True)


if __name__ == "__main__":
    raise SystemExit(main())
