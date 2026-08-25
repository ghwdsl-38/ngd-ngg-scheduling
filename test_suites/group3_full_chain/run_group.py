#!/usr/bin/env python3
"""第三组：分离完整链路计时与 NGD/Algorithm/NGG 证据。"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path

GROUP_DIR = Path(__file__).resolve().parent
SUITES_DIR = GROUP_DIR.parent
PROJECT_ROOT = SUITES_DIR.parent
sys.path.insert(0, str(SUITES_DIR))

from common.fixture import generate, load_config, write_fixture  # noqa: E402
from common.mock_prometheus import start as start_prometheus  # noqa: E402
from common.runtime import run_id, write_json  # noqa: E402

HTTP_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def docker(*args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(["docker", *args], check=check, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)


def wait_algorithm(url: str) -> None:
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        try:
            with HTTP_OPENER.open(url + "/healthz", timeout=2) as response:
                if json.loads(response.read()).get("status") == "ok":
                    return
        except Exception:
            time.sleep(0.1)
    raise RuntimeError("Algorithm health timeout")


def start_algorithm(image: str, prometheus_port: int, token_file: Path) -> tuple[str, str]:
    if shutil.which("docker") is None:
        raise RuntimeError("docker is required")
    if docker("image", "inspect", image, check=False).returncode:
        raise RuntimeError(f"missing image {image}; run make algorithm-image")
    port = free_port()
    name = f"ngd-ngg-test-g3-{os.getpid()}-{port}"
    docker(
        "run", "--detach", "--name", name,
        "--add-host", "host.docker.internal:host-gateway",
        "--publish", f"127.0.0.1:{port}:8080",
        "--volume", f"{token_file}:/run/secrets/prometheus-token:ro",
        "--env", f"PROMETHEUS_URL=http://host.docker.internal:{prometheus_port}",
        "--env", "PROMETHEUS_BEARER_TOKEN_FILE=/run/secrets/prometheus-token",
        "--env", "PROMETHEUS_REFRESH_SECONDS=1",
        "--env", "PROMETHEUS_STALE_SECONDS=120",
        "--env", "PROMETHEUS_REQUEST_TIMEOUT_SECONDS=30",
        image,
    )
    url = f"http://127.0.0.1:{port}"
    wait_algorithm(url)
    return name, url


def build_runner(group_run: Path) -> Path:
    binary = PROJECT_ROOT / "build/test-group2-prc"
    process = subprocess.run([
        "docker", "run", "--rm", "--volume", f"{PROJECT_ROOT}:/src",
        "--volume", f"{PROJECT_ROOT / '.cache/go-mod'}:/go/pkg/mod",
        "--volume", f"{PROJECT_ROOT / '.cache/go-build'}:/root/.cache/go-build",
        "--workdir", "/src/test_suites/group2_prc/runner", "--env", "CGO_ENABLED=0",
        "--env", "GOMAXPROCS=2", "golang:1.25-alpine", "go", "build", "-p=1",
        "-o", "/src/build/test-group2-prc", ".",
    ], text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    (group_run / "logs").mkdir(parents=True, exist_ok=True)
    (group_run / "logs" / "runner-build.log").write_text(process.stdout, encoding="utf-8")
    if process.returncode:
        raise RuntimeError("Group3 runner build failed; see logs/runner-build.log")
    return binary


def run_mode(binary: Path, assets: Path, fixture: dict, fixture_dir: Path, group_run: Path, image: str, token_file: Path, mode: str) -> dict:
    output = group_run / f"{mode}-run"
    catalogue = json.loads((PROJECT_ROOT / "algorithm_server/go/prometheus_metrics.json").read_text(encoding="utf-8"))
    audit = output / "infrastructure/prometheus-requests.jsonl" if mode == "evidence" else None
    prometheus, _ = start_prometheus(fixture["metrics"], catalogue, load_config()["prometheus"]["bearerToken"], audit)
    container = ""
    try:
        container, algorithm_url = start_algorithm(image, int(prometheus.server_address[1]), token_file)
        environment = dict(os.environ)
        environment["KUBEBUILDER_ASSETS"] = str(assets)
        process = subprocess.run([
            str(binary), "--suite", "group3", "--mode", mode,
            "--project-root", str(PROJECT_ROOT), "--fixture-dir", str(fixture_dir),
            "--output-dir", str(output), "--algorithm-url", algorithm_url,
            "--prometheus-control-url", f"http://127.0.0.1:{prometheus.server_address[1]}",
        ], env=environment, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        (group_run / "logs" / f"runner-{mode}.log").write_text(process.stdout, encoding="utf-8")
        if process.returncode:
            raise RuntimeError(f"Group3 {mode} runner failed; see logs/runner-{mode}.log")
        result_file = output / ("timing-result.json" if mode == "timing" else "evidence-result.json")
        return json.loads(result_file.read_text(encoding="utf-8"))
    finally:
        if container:
            logs = docker("logs", container, check=False).stdout
            if mode == "evidence":
                (output / "logs").mkdir(parents=True, exist_ok=True)
                (output / "logs" / "algorithm-server.log").write_text(logs, encoding="utf-8")
            docker("rm", "--force", container, check=False)
        prometheus.shutdown()
        prometheus.server_close()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("timing", "evidence", "all"), default="all")
    parser.add_argument("--image", default=os.getenv("ALGORITHM_IMAGE", "ngd-ngg-algorithm:v0.4.0"))
    args = parser.parse_args()
    group_run = GROUP_DIR / "runs" / run_id()
    group_run.mkdir(parents=True, exist_ok=True)
    assets = Path(os.getenv("KUBEBUILDER_ASSETS", str(PROJECT_ROOT / ".cache/envtest/1.35.5"))).resolve()
    missing = [str(assets / name) for name in ("kube-apiserver", "etcd", "kubectl") if not (assets / name).is_file()]
    if missing:
        write_json(group_run / "result.json", {"status": "BLOCKED", "missing": missing})
        return 2
    try:
        binary = build_runner(group_run)
        fixture = generate(load_config())
        with tempfile.TemporaryDirectory(prefix="ngd-ngg-group3-fixture-") as temporary:
            fixture_dir = Path(temporary)
            write_fixture(fixture_dir, fixture, {"kubernetes-nodes.json", "kubernetes-pods.json"})
            token_file = fixture_dir / "prometheus-token"
            token_file.write_text(load_config()["prometheus"]["bearerToken"] + "\n", encoding="utf-8")
            # 容器以65532非root用户运行，临时目录退出时会整体删除。
            token_file.chmod(0o644)
            timing = run_mode(binary, assets, fixture, fixture_dir, group_run, args.image, token_file, "timing") if args.mode in ("timing", "all") else None
            evidence = run_mode(binary, assets, fixture, fixture_dir, group_run, args.image, token_file, "evidence") if args.mode in ("evidence", "all") else None
        if timing and evidence:
            timing_outputs = {item["id"]: item["output"] for item in timing["cases"]}
            evidence_outputs = {item["id"]: item["output"] for item in evidence["cases"]}
            assert timing_outputs == evidence_outputs
        summary = {
            "status": "PASS", "mode": args.mode, "cases": ["cold_cache", "warm_cache"],
            "fixtureNodeCount": fixture["summary"]["nodeCount"],
            "timingRun": str(group_run / "timing-run") if timing else None,
            "evidenceRun": str(group_run / "evidence-run") if evidence else None,
            "timingAndEvidenceCoreResultsEqual": bool(timing and evidence),
        }
        write_json(group_run / "result.json", summary)
        (group_run / "report.md").write_text(
            "# 第三组：完整链路\n\n"
            "- Timing Run：从 PRC Watch NGD 到 NGG 生效并完成模拟 Binding。\n"
            "- Evidence Run：输出 NGD YAML、Algorithm JSON结果、NGG YAML和Pod绑定结果。\n",
            encoding="utf-8",
        )
        print(json.dumps({"status": "PASS", "result": str(group_run)}, ensure_ascii=False))
        return 0
    except BaseException as exc:
        write_json(group_run / "result.json", {"status": "FAIL", "mode": args.mode, "error": str(exc)})
        print("FAIL: " + str(exc), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
