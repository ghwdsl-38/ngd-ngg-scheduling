#!/usr/bin/env python3
"""使用 1000 个内存模拟 Node，通过真实 HTTP/容器验证 Algorithm Server。

该脚本不会创建 1000 个 Kubernetes Node。它模拟 PRC 和 Prometheus 的输入，
启动一个正式 Algorithm 镜像，发送两次请求并把全部输入、输出和日志写入 results。
"""

from __future__ import annotations

import copy
import hashlib
import json
import os
import shutil
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any


DEMO_DIR = Path(__file__).resolve().parent
PROJECT_ROOT = DEMO_DIR.parents[1]
RESULTS_DIR = Path(
    os.getenv(
        "ALGORITHM_1000_RESULTS_DIR",
        str(PROJECT_ROOT / "results" / "algorithm-1000-nodes"),
    )
)
ALGORITHM_IMAGE = os.getenv(
    "ALGORITHM_IMAGE",
    "ngd-ngg-algorithm:v0.4.0",
)
NODE_COUNT = 1_000
CORE_COUNT = 4
LEAVES_PER_CORE = 25
NODES_PER_LEAF = 10


def canonical_hash(value: Any) -> str:
    """按协议使用稳定 JSON 编码计算 Node 静态快照身份。"""

    encoded = json.dumps(
        value,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return "sha256:" + hashlib.sha256(encoded).hexdigest()


def build_demo_data() -> tuple[
    dict[str, Any],
    dict[str, Any],
    list[dict[str, Any]],
    dict[str, dict[str, float]],
    dict[str, Any],
]:
    """生成Leaf静态节点、联通式上层拓扑、任务级状态和指标。"""

    nodes: list[dict[str, Any]] = []
    usage_states: list[dict[str, Any]] = []
    metrics: dict[str, dict[str, float]] = {}
    cores: dict[str, dict[str, Any]] = {}

    # 4 Core × 25 Leaf × 10 Node = 1000 Node。
    for node_number in range(1, NODE_COUNT + 1):
        zero_based = node_number - 1
        leaf_number = zero_based // NODES_PER_LEAF + 1
        position = zero_based % NODES_PER_LEAF + 1
        core_number = (leaf_number - 1) // LEAVES_PER_CORE + 1
        node_name = f"worker-{node_number:04d}"
        node_uid = f"uid-worker-{node_number:04d}"
        core_id = f"core-{core_number:02d}"
        leaf_id = f"leaf-{leaf_number:03d}"

        bandwidth = (25, 40, 50, 100)[(leaf_number - 1) % 4]
        latency = (4.0, 2.5, 1.5, 0.8)[(leaf_number - 1) % 4]
        nodes.append(
            {
                "nodeName": node_name,
                "nodeUID": node_uid,
                "allocatable": {
                    "cpu": "32",
                    "memory": "128Gi",
                    "nvidia.com/gpu": "4",
                },
                "labels": {
                    "demo.ngg/worker": "true",
                    "topology.demo.ngg.io/leaf-switch": leaf_id,
                },
                "topology": {
                    "leafSwitchId": leaf_id,
                    "switchId": leaf_id,
                },
            }
        )

        # 每 5 个节点模拟 1 个正被其他任务占用的节点，共 200 个。
        usage_states.append(
            {
                "nodeUID": node_uid,
                "inUse": node_number % 5 == 0,
            }
        )

        # 生成确定性的 CPU/内存利用率，便于重复演示得到稳定排序。
        cpu = round(
            0.08 + ((leaf_number * 13 + position * 7) % 65) / 100,
            3,
        )
        memory = round(
            0.10 + ((leaf_number * 11 + position * 5) % 60) / 100,
            3,
        )
        metrics[node_name] = {
            "cpuUsageRatio": min(cpu, 0.95),
            "memoryUsageRatio": min(memory, 0.95),
        }

        core = cores.setdefault(
            core_id,
            {"coreSwitchId": core_id, "leafSwitches": {}},
        )
        leaf = core["leafSwitches"].setdefault(
            leaf_id,
            {
                "leafSwitchId": leaf_id,
                "bandwidthGbps": bandwidth,
                "latencyMillis": latency,
                "nodeNames": [],
            },
        )
        leaf["nodeNames"].append(node_name)

    topology = {
        "model": "region-location-dc-room-border-domain-leaf-node",
        "regionId": "CN-NORTH",
        "locationId": "HB-HL",
        "dataCenterId": "HB-HL-DC1",
        "roomId": "HB-HL-DC1-102",
        "borderDomainCount": CORE_COUNT,
        "leafSwitchCount": CORE_COUNT * LEAVES_PER_CORE,
        "nodeCount": NODE_COUNT,
        "borderDomains": [
            {
                "borderDomainId": (
                    f"HB-HL-DC1-102-BORDER-DOMAIN-{index:02d}"
                ),
                "spines": {},
                "leafSwitches": [
                    core["leafSwitches"][leaf_id]
                    for leaf_id in sorted(core["leafSwitches"])
                ],
            }
            for index, (_core_id, core) in enumerate(
                sorted(cores.items()), start=1
            )
        ],
    }
    static_snapshot = {
        "clusterId": "algorithm-1000-node-demo",
        "topologyVersion": "node-leaf-v1",
        "nodes": nodes,
    }
    borders_by_domain = {
        f"HB-HL-DC1-102-BORDER-DOMAIN-{number:02d}": [
            f"HB-HL-DC1-102-BD{number:02d}-BORDER-SW01-ZTE9904X",
            f"HB-HL-DC1-102-BD{number:02d}-BORDER-SW02-ZTE9904X",
        ]
        for number in range(1, CORE_COUNT + 1)
    }
    room: dict[str, Any] = {}
    leaf_metrics: dict[str, Any] = {}
    for leaf_number in range(1, CORE_COUNT * LEAVES_PER_CORE + 1):
        leaf_id = f"leaf-{leaf_number:03d}"
        domain_number = (leaf_number - 1) // LEAVES_PER_CORE + 1
        domain_id = f"HB-HL-DC1-102-BORDER-DOMAIN-{domain_number:02d}"
        room[leaf_id] = {
            "SPINE": {},
            "BORDER": {
                border: {
                    "local_port": f"cgei-0/1/1/{51 + index * 2}",
                    "peer_port": f"cgei-0/3/0/{leaf_number}",
                }
                for index, border in enumerate(borders_by_domain[domain_id])
            },
            "LEAF": {},
        }
        leaf_metrics[leaf_id] = {
            "bandwidthGbps": (25, 40, 50, 100)[(leaf_number - 1) % 4],
            "latencyMillis": (4.0, 2.5, 1.5, 0.8)[(leaf_number - 1) % 4],
        }
    topology_config = {
        "version": "unicom-border-domain-demo-v1",
        "scopes": {
            "regions": [{"id": "CN-NORTH", "name": "华北"}],
            "locations": [
                {"id": "HB-HL", "name": "怀来", "regionId": "CN-NORTH"}
            ],
            "dataCenters": [{"id": "HB-HL-DC1", "locationId": "HB-HL"}],
            "rooms": [
                {"id": "HB-HL-DC1-102", "dataCenterId": "HB-HL-DC1"}
            ],
        },
        "borderDomains": {
            domain_id: {
                "roomId": "HB-HL-DC1-102",
                "mode": "exact-set",
                "members": members,
            }
            for domain_id, members in borders_by_domain.items()
        },
        "leafMetrics": leaf_metrics,
        "topology": {"HB-HL-DC1-102": room},
    }
    return static_snapshot, topology, usage_states, metrics, topology_config


class MockPrometheusHandler(BaseHTTPRequestHandler):
    """提供与 Prometheus instant-query vector 相同格式的临时 HTTP 服务。"""

    metrics: dict[str, dict[str, float]] = {}
    query_count = 0

    def do_GET(self) -> None:
        """根据 PromQL 中是否含 cpu 返回相应的 1000 Node 指标。"""

        parsed = urllib.parse.urlparse(self.path)
        if parsed.path != "/api/v1/query":
            self.send_error(404)
            return

        query = urllib.parse.parse_qs(parsed.query).get("query", [""])[0]
        field = (
            "cpuUsageRatio"
            if "cpu" in query.lower()
            else "memoryUsageRatio"
        )
        timestamp = int(time.time())
        result = [
            {
                "metric": {"node": node_name},
                "value": [timestamp, str(values[field])],
            }
            for node_name, values in sorted(self.metrics.items())
        ]
        payload = json.dumps(
            {
                "status": "success",
                "data": {
                    "resultType": "vector",
                    "result": result,
                },
            },
            separators=(",", ":"),
        ).encode("utf-8")
        type(self).query_count += 1
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        try:
            self.wfile.write(payload)
        except (BrokenPipeError, ConnectionResetError):
            # Algorithm停止或刷新超时后可能主动关闭连接，不影响Mock数据正确性。
            pass

    def log_message(self, _format: str, *args: Any) -> None:
        del args


def start_mock_prometheus(
    metrics: dict[str, dict[str, float]],
) -> tuple[ThreadingHTTPServer, threading.Thread]:
    """在随机空闲端口启动后台模拟 Prometheus。"""

    MockPrometheusHandler.metrics = metrics
    MockPrometheusHandler.query_count = 0
    server = ThreadingHTTPServer(
        ("0.0.0.0", 0),
        MockPrometheusHandler,
    )
    thread = threading.Thread(
        target=server.serve_forever,
        name="mock-prometheus",
        daemon=True,
    )
    thread.start()
    return server, thread


def free_port() -> int:
    """向操作系统申请一个用于临时 Algorithm HTTP 映射的空闲端口。"""

    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def docker(*arguments: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    """执行 Docker 命令并统一捕获标准输出，便于写入演示证据。"""

    return subprocess.run(
        ["docker", *arguments],
        check=check,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )


def start_algorithm_container(
    prometheus_port: int, topology_config_path: Path
) -> tuple[str, int]:
    """启动正式 Algorithm 镜像，并让容器访问宿主机模拟 Prometheus。"""

    if shutil.which("docker") is None:
        raise RuntimeError("缺少 docker，请先安装 Docker")
    inspected = docker(
        "image",
        "inspect",
        ALGORITHM_IMAGE,
        check=False,
    )
    if inspected.returncode != 0:
        raise RuntimeError(
            f"未找到镜像 {ALGORITHM_IMAGE}，请先运行 make algorithm-image"
        )

    api_port = free_port()
    container_name = f"algorithm-1000-demo-{os.getpid()}"
    result = docker(
        "run",
        "--detach",
        "--rm",
        "--name",
        container_name,
        "--add-host",
        "host.docker.internal:host-gateway",
        "--publish",
        f"127.0.0.1:{api_port}:8080",
        "--env",
        f"PROMETHEUS_URL=http://host.docker.internal:{prometheus_port}",
        "--env",
        "PROMETHEUS_REFRESH_SECONDS=1",
        "--env",
        "PROMETHEUS_STALE_SECONDS=60",
        "--env",
        "PROMETHEUS_REQUEST_TIMEOUT_SECONDS=5",
        "--env",
        "TOPOLOGY_CONFIG_FILE=/etc/ngd-ngg/topology.yaml",
        "--volume",
        f"{topology_config_path.resolve()}:/etc/ngd-ngg/topology.yaml:ro",
        ALGORITHM_IMAGE,
    )
    if not result.stdout.strip():
        raise RuntimeError("Algorithm 容器启动失败")
    return container_name, api_port


def http_json(
    method: str,
    url: str,
    payload: dict[str, Any] | None = None,
    timeout: float = 10,
) -> dict[str, Any]:
    """发送 JSON HTTP 请求；非 2xx 响应转换为包含响应体的异常。"""

    body = (
        json.dumps(payload, separators=(",", ":")).encode("utf-8")
        if payload is not None
        else None
    )
    request = urllib.request.Request(
        url,
        data=body,
        method=method,
        headers={
            "Accept": "application/json",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return json.loads(response.read())
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(
            f"{method} {url} 返回 HTTP {exc.code}: {detail}"
        ) from exc


def wait_for_api(base_url: str, timeout_seconds: float = 30) -> None:
    """轮询 healthz，避免容器已启动但 HTTP 尚未监听的竞态。"""

    deadline = time.monotonic() + timeout_seconds
    last_error = ""
    while time.monotonic() < deadline:
        try:
            if http_json("GET", f"{base_url}/healthz")["status"] == "ok":
                return
        except Exception as exc:
            last_error = str(exc)
        time.sleep(0.2)
    raise RuntimeError(f"等待 Algorithm API 超时: {last_error}")


def wait_for_metrics(
    base_url: str,
    expected_nodes: int,
    timeout_seconds: float = 30,
) -> dict[str, Any]:
    """等待 Go 指标缓存完整获取 1000 个 Node 且不处于降级状态。"""

    deadline = time.monotonic() + timeout_seconds
    last_status: dict[str, Any] = {}
    while time.monotonic() < deadline:
        last_status = http_json(
            "GET",
            f"{base_url}/internal/v1/cache/status",
        )
        metrics = last_status["metrics"]
        if (
            metrics["ready"]
            and not metrics["degraded"]
            and metrics["nodeCount"] == expected_nodes
        ):
            return last_status
        time.sleep(0.2)
    raise RuntimeError(
        "Prometheus 指标缓存未就绪: "
        + json.dumps(last_status, ensure_ascii=False)
    )


def write_json(name: str, value: Any) -> None:
    """把一份可阅读的演示输入或输出写入固定 results 目录。"""

    path = RESULTS_DIR / name
    path.write_text(
        json.dumps(value, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )


def build_request(
    snapshot_id: str,
    usage_states: list[dict[str, Any]],
) -> dict[str, Any]:
    """构造严格使用联通 NGD 字段的资源池算法请求。"""

    return {
        "requestId": "algorithm-1000-request-1",
        "taskUID": "task-algorithm-1000",
        "ngdUID": "ngd-algorithm-1000",
        "ngdGeneration": 1,
        "requestMode": "resourcePool",
        "nodeStaticSnapshotId": snapshot_id,
        "nodeUsageStates": usage_states,
        "ngd": {
            "schedulerName": "volcano",
            "nodeSelector": {
                "matchLabels": {"demo.ngg/worker": "true"}
            },
            # 每Leaf只有8个可用Node，15个Node的最低需求会跳过空SPINE，
            # 在独立拓扑配置定义的Border Domain层找到候选。
            "maxNodes": 18,
            "quota": {"cpu": "576", "memory": "2304Gi"},
            "minResources": {"cpu": "480", "memory": "1920Gi"},
        },
    }


def verify_response(
    response: dict[str, Any],
    snapshot_id: str,
    forbidden_group: str = "",
) -> None:
    """断言响应身份、Top-3排序、Border Domain和动态排除结果。"""

    assert response["status"] == "SUCCESS", response
    assert response["nodeStaticSnapshotId"] == snapshot_id
    assert response["metricsSnapshotId"] != "metrics-disabled"
    assert response["degraded"] is False
    candidates = response["candidateNodeGroups"]
    assert len(candidates) == 3
    assert [item["rank"] for item in candidates] == [1, 2, 3]
    scores = [item["groupScore"] for item in candidates]
    assert scores == sorted(scores, reverse=True)
    assert all(item["topologyLevel"] == "borderDomain" for item in candidates)
    if forbidden_group:
        assert forbidden_group not in {
            item["groupId"] for item in candidates
        }


def candidate_summary(response: dict[str, Any]) -> list[dict[str, Any]]:
    """抽取便于人阅读的候选组摘要，不丢弃原始完整响应文件。"""

    return [
        {
            "rank": item["rank"],
            "groupId": item["groupId"],
            "topologyLevel": item["topologyLevel"],
            "groupScore": item["groupScore"],
            "availableNodeCount": len(item["nodes"]),
        }
        for item in response["candidateNodeGroups"]
    ]


def main() -> int:
    """编排数据生成、服务启动、两次计算、证据落盘和资源清理。"""

    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    (
        static_snapshot,
        topology,
        usage_states,
        metrics,
        topology_config,
    ) = build_demo_data()
    snapshot_id = canonical_hash(static_snapshot)
    unavailable_count = sum(
        1 for item in usage_states if item["inUse"]
    )

    write_json(
        "node-static-snapshot.json",
        {
            "snapshotId": snapshot_id,
            **static_snapshot,
        },
    )
    write_json("topology.json", topology)
    write_json("network-topology.yaml", topology_config)
    write_json(
        "prometheus-metrics.json",
        {
            "source": "mock-prometheus-http-api",
            "nodeCount": len(metrics),
            "nodes": metrics,
        },
    )
    write_json(
        "node-dynamic-state.json",
        {
            "scope": "request",
            "nodeCount": len(usage_states),
            "inUseCount": unavailable_count,
            "nodes": usage_states,
        },
    )

    prometheus, prometheus_thread = start_mock_prometheus(metrics)
    container_name = ""
    started_at = time.perf_counter()
    try:
        prometheus_port = int(prometheus.server_address[1])
        container_name, api_port = start_algorithm_container(
            prometheus_port, RESULTS_DIR / "network-topology.yaml"
        )
        base_url = f"http://127.0.0.1:{api_port}"
        wait_for_api(base_url)
        cache_status = wait_for_metrics(base_url, NODE_COUNT)

        encoded_id = urllib.parse.quote(snapshot_id, safe="")
        static_ack = http_json(
            "PUT",
            (
                f"{base_url}/internal/v1/node-static-snapshots/"
                f"{encoded_id}"
            ),
            static_snapshot,
            timeout=30,
        )
        assert static_ack["nodeCount"] == NODE_COUNT
        assert static_ack["snapshotId"] == snapshot_id
        cache_status = http_json(
            "GET",
            f"{base_url}/internal/v1/cache/status",
        )
        assert cache_status["nodeStatic"]["ready"] is True
        assert (
            cache_status["nodeStatic"]["nodeCount"] == NODE_COUNT
        )

        first_request = build_request(snapshot_id, usage_states)
        write_json("allocation-request-1.json", first_request)
        first_response = http_json(
            "POST",
            f"{base_url}/api/v1/allocate",
            first_request,
            timeout=30,
        )
        verify_response(first_response, snapshot_id)
        write_json("allocation-response-1.json", first_response)

        # 把第一候选Border Domain中的全部节点标为占用，验证动态状态
        # 不进入跨请求缓存，并且第二次请求会重新过滤该Domain。
        first_group = first_response["candidateNodeGroups"][0]
        selected_domain = first_group["groupId"].removeprefix(
            "border-domain:"
        )
        selected_members = set(
            topology_config["borderDomains"][selected_domain]["members"]
        )
        room_topology = topology_config["topology"]["HB-HL-DC1-102"]
        selected_leaves = {
            leaf
            for leaf, adjacency in room_topology.items()
            if set(adjacency["BORDER"]) == selected_members
        }
        newly_busy = {
            node["nodeUID"]
            for node in static_snapshot["nodes"]
            if node["topology"]["leafSwitchId"] in selected_leaves
        }
        second_request = copy.deepcopy(first_request)
        second_request["requestId"] = "algorithm-1000-request-2"
        for item in second_request["nodeUsageStates"]:
            if item["nodeUID"] in newly_busy:
                item["inUse"] = True
        write_json("allocation-request-2.json", second_request)
        second_response = http_json(
            "POST",
            f"{base_url}/api/v1/allocate",
            second_request,
            timeout=30,
        )
        verify_response(
            second_response,
            snapshot_id,
            forbidden_group=first_group["groupId"],
        )
        write_json("allocation-response-2.json", second_response)

        elapsed_ms = round(
            (time.perf_counter() - started_at) * 1000,
            2,
        )
        summary = {
            "status": "PASS",
            "algorithmImage": ALGORITHM_IMAGE,
            "elapsedMilliseconds": elapsed_ms,
            "staticSnapshotId": snapshot_id,
            "staticNodeCount": len(static_snapshot["nodes"]),
            "topology": {
                "levels": [
                    "region", "location", "dataCenter", "room",
                    "borderDomain", "leafSwitch", "node",
                ],
                "borderDomainCount": CORE_COUNT,
                "leafSwitchCount": CORE_COUNT * LEAVES_PER_CORE,
                "nodeCount": NODE_COUNT,
            },
            "prometheus": {
                "nodeMetricCount": len(metrics),
                "queryCount": MockPrometheusHandler.query_count,
                "metricsSnapshotId": first_response[
                    "metricsSnapshotId"
                ],
                "degraded": first_response["degraded"],
            },
            "dynamicState": {
                "request1InUseCount": unavailable_count,
                "request2InUseCount": unavailable_count
                + len(newly_busy),
                "requestScoped": True,
            },
            "request1Candidates": candidate_summary(first_response),
            "request2Candidates": candidate_summary(second_response),
            "removedAfterDynamicStateChange": first_group["groupId"],
            "cacheStatus": cache_status,
        }
        write_json("demo-summary.json", summary)

        print("Algorithm API Server 1000 节点独立演示：PASS")
        print(
            f"静态节点={NODE_COUNT}，BorderDomain={CORE_COUNT}，"
            f"Leaf={CORE_COUNT * LEAVES_PER_CORE}"
        )
        print(
            f"Prometheus节点指标={len(metrics)}，"
            f"查询次数={MockPrometheusHandler.query_count}"
        )
        print(
            "第一次Top-3="
            + " -> ".join(
                item["groupId"]
                for item in first_response["candidateNodeGroups"]
            )
        )
        print(
            f"标记 {first_group['groupId']} 为占用后，第二次Top-3="
            + " -> ".join(
                item["groupId"]
                for item in second_response["candidateNodeGroups"]
            )
        )
        print(f"结果目录：{RESULTS_DIR}")
        return 0
    finally:
        if container_name:
            logs = docker(
                "logs",
                container_name,
                check=False,
            ).stdout
            (RESULTS_DIR / "algorithm-server.log").write_text(
                logs,
                encoding="utf-8",
            )
            docker(
                "stop",
                "--time",
                "3",
                container_name,
                check=False,
            )
        prometheus.shutdown()
        prometheus.server_close()
        prometheus_thread.join(timeout=3)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (AssertionError, RuntimeError) as exc:
        print(f"Algorithm API Server 1000 节点独立演示：FAIL: {exc}", file=sys.stderr)
        raise SystemExit(1)
