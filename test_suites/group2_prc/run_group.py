#!/usr/bin/env python3
"""第二组：仅演示一次正常 NGD→PRC→Mock Algorithm→NGG。"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

GROUP_DIR = Path(__file__).resolve().parent
SUITES_DIR = GROUP_DIR.parent
PROJECT_ROOT = SUITES_DIR.parent
sys.path.insert(0, str(SUITES_DIR))

from common.fixture import generate, load_config, write_fixture  # noqa: E402
from common.runtime import run_id, write_json  # noqa: E402


def build_runner(group_run: Path) -> Path:
    binary = PROJECT_ROOT / "build/test-group2-prc"
    command = [
        "docker", "run", "--rm", "--volume", f"{PROJECT_ROOT}:/src",
        "--volume", f"{PROJECT_ROOT / '.cache/go-mod'}:/go/pkg/mod",
        "--volume", f"{PROJECT_ROOT / '.cache/go-build'}:/root/.cache/go-build",
        "--workdir", "/src/test_suites/group2_prc/runner", "--env", "CGO_ENABLED=0",
        "--env", "GOMAXPROCS=2", "golang:1.25-alpine", "go", "build", "-p=1",
        "-o", "/src/build/test-group2-prc", ".",
    ]
    build = subprocess.run(command, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    (group_run / "logs").mkdir(parents=True, exist_ok=True)
    (group_run / "logs" / "runner-build.log").write_text(build.stdout, encoding="utf-8")
    if build.returncode:
        raise RuntimeError("Group2 runner build failed; see logs/runner-build.log")
    return binary


def run_mode(binary: Path, assets: Path, fixture_dir: Path, group_run: Path, mode: str) -> dict:
    output = group_run / f"{mode}-run"
    environment = dict(os.environ)
    environment["KUBEBUILDER_ASSETS"] = str(assets)
    process = subprocess.run(
        [str(binary), "--suite", "group2", "--mode", mode, "--project-root", str(PROJECT_ROOT), "--fixture-dir", str(fixture_dir), "--output-dir", str(output)],
        env=environment, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )
    (group_run / "logs" / f"runner-{mode}.log").write_text(process.stdout, encoding="utf-8")
    if process.returncode:
        raise RuntimeError(f"Group2 {mode} runner failed; see logs/runner-{mode}.log")
    result_file = output / ("timing-result.json" if mode == "timing" else "evidence-result.json")
    return json.loads(result_file.read_text(encoding="utf-8"))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("timing", "evidence", "all"), default="all")
    args = parser.parse_args()
    group_run = GROUP_DIR / "runs" / run_id()
    group_run.mkdir(parents=True, exist_ok=True)
    assets = Path(os.getenv("KUBEBUILDER_ASSETS", str(PROJECT_ROOT / ".cache/envtest/1.35.5"))).resolve()
    missing = [str(assets / name) for name in ("kube-apiserver", "etcd", "kubectl") if not (assets / name).is_file()]
    if missing:
        write_json(group_run / "result.json", {"status": "BLOCKED", "missing": missing})
        print("BLOCKED: envtest assets missing: " + ", ".join(missing), file=sys.stderr)
        return 2
    try:
        binary = build_runner(group_run)
        fixture = generate(load_config())
        with tempfile.TemporaryDirectory(prefix="ngd-ngg-group2-fixture-") as temporary:
            fixture_dir = Path(temporary)
            write_fixture(fixture_dir, fixture, {"kubernetes-nodes.json", "kubernetes-pods.json"})
            timing = run_mode(binary, assets, fixture_dir, group_run, "timing") if args.mode in ("timing", "all") else None
            evidence = run_mode(binary, assets, fixture_dir, group_run, "evidence") if args.mode in ("evidence", "all") else None
        if timing and evidence:
            assert timing["cases"][0]["output"]["nodeCount"] == evidence["cases"][0]["output"]["nodeCount"]
        summary = {
            "status": "PASS", "mode": args.mode, "case": "normal_create",
            "fixtureNodeCount": fixture["summary"]["nodeCount"],
            "timingRun": str(group_run / "timing-run") if timing else None,
            "evidenceRun": str(group_run / "evidence-run") if evidence else None,
            "timingAndEvidenceCoreResultsEqual": bool(timing and evidence),
        }
        write_json(group_run / "result.json", summary)
        (group_run / "report.md").write_text(
            "# 第二组：PRC normal_create\n\n"
            "- 第一遍只计算 PRC Watch NGD 到 NGG Ready 的时间。\n"
            "- 第二遍输出 NGD YAML、PRC 静态/动态请求、Mock Algorithm 响应和 NGG YAML。\n"
            "- 两遍均使用相同的 3000 Node 确定性 Fixture。\n",
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
