#!/usr/bin/env python3
"""从一个固定规则生成三组共用的 3000 Node 静态、动态和指标数据。"""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any

from .runtime import write_json


METRIC_NAMES = (
    "cpuUsageRatio", "memoryUsageRatio",
    "networkReceiveBytesPerSecond", "networkTransmitBytesPerSecond",
    "networkReceivePacketsPerSecond", "networkTransmitPacketsPerSecond",
    "networkReceiveDropRatio", "networkTransmitDropRatio",
    "networkReceiveErrorRatio", "networkTransmitErrorRatio",
    "tcpRetransmitRatio", "networkLinkUpRatio", "networkUtilizationRatio",
    "availableBandwidthBytesPerSecond",
)


def canonical_hash(value: Any) -> str:
    raw = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def load_config(path: Path | None = None) -> dict[str, Any]:
    source = path or Path(__file__).with_name("fixture-config.json")
    return json.loads(source.read_text(encoding="utf-8"))


def generate(config: dict[str, Any]) -> dict[str, Any]:
    count = int(config["nodeCount"])
    topology_config = config["topology"]
    nodes_per_leaf = int(topology_config["nodesPerLeaf"])
    leaf_count = int(topology_config["leafCount"])
    border_count = int(topology_config["borderCount"])
    leaves_per_core = leaf_count // int(topology_config["coreCount"])
    nodes: list[dict[str, Any]] = []
    states: list[dict[str, Any]] = []
    metrics: dict[str, dict[str, float]] = {}
    kubernetes_nodes: list[dict[str, Any]] = []
    kubernetes_pods: list[dict[str, Any]] = []

    for number in range(1, count + 1):
        index = number - 1
        leaf_number = index // nodes_per_leaf + 1
        core_number = (leaf_number - 1) // leaves_per_core + 1
        # 150 Leaf 均匀挂到 4 个 Border：38、37、38、37。
        border_number = min(border_count, (leaf_number - 1) * border_count // leaf_count + 1)
        node_name = f"worker-{number:04d}"
        uid = f"uid-worker-{number:04d}"
        leaf = f"leaf-{leaf_number:03d}"
        border = f"border-{border_number:02d}"
        core = f"core-{core_number:02d}"
        bandwidth = float((25, 40, 50, 100)[(leaf_number - 1) % 4])
        latency = float((4.0, 2.5, 1.5, 0.8)[(leaf_number - 1) % 4])
        labels = {
            "tests.ngg.io/worker": "true",
            "topology.demo.ngg.io/topology-version": "dc-core-border-leaf-v1",
            "topology.demo.ngg.io/core-switch": core,
            "topology.demo.ngg.io/border-switch": border,
            "topology.demo.ngg.io/leaf-switch": leaf,
            "topology.demo.ngg.io/bandwidth-gbps": str(bandwidth),
            "topology.demo.ngg.io/latency-ms": str(latency),
        }
        topology = {
            "dataCenter": topology_config["dataCenter"],
            "coreSwitchId": core,
            "borderSwitchId": border,
            "leafSwitchId": leaf,
            "bandwidthGbps": bandwidth,
            "latencyMillis": latency,
        }
        node = {
            "nodeName": node_name,
            "nodeUID": uid,
            "createdAt": "2026-08-19T00:00:00Z",
            "allocatable": {"cpu": "32", "memory": "128Gi", "nvidia.com/gpu": "4"},
            "labels": labels,
            "topology": topology,
        }
        nodes.append(node)
        in_use = number % int(config["dynamicState"]["inUseEveryNthNode"]) == 0
        states.append({"nodeUID": uid, "inUse": in_use})
        cpu = round(0.08 + ((leaf_number * 13 + number * 7) % 65) / 100, 4)
        memory = round(0.10 + ((leaf_number * 11 + number * 5) % 60) / 100, 4)
        receive_bps = float(20_000_000 + (number * 7919) % 600_000_000)
        transmit_bps = float(15_000_000 + (number * 6151) % 500_000_000)
        receive_pps = float(20_000 + (number * 97) % 800_000)
        transmit_pps = float(18_000 + (number * 89) % 700_000)
        utilization = min(0.95, (receive_bps + transmit_bps) / (bandwidth * 125_000_000))
        metrics[node_name] = {
            "cpuUsageRatio": min(cpu, 0.95),
            "memoryUsageRatio": min(memory, 0.95),
            "networkReceiveBytesPerSecond": receive_bps,
            "networkTransmitBytesPerSecond": transmit_bps,
            "networkReceivePacketsPerSecond": receive_pps,
            "networkTransmitPacketsPerSecond": transmit_pps,
            "networkReceiveDropRatio": round((number % 11) / 10000, 6),
            "networkTransmitDropRatio": round((number % 13) / 10000, 6),
            "networkReceiveErrorRatio": round((number % 7) / 20000, 6),
            "networkTransmitErrorRatio": round((number % 5) / 20000, 6),
            "tcpRetransmitRatio": round((number % 17) / 10000, 6),
            "networkLinkUpRatio": 1.0 if number % 101 else 0.5,
            "networkUtilizationRatio": round(utilization, 6),
            "availableBandwidthBytesPerSecond": max(0.0, bandwidth * 125_000_000 - receive_bps - transmit_bps),
        }
        kubernetes_nodes.append({
            "apiVersion": "v1", "kind": "Node",
            "metadata": {"name": node_name, "uid": uid, "labels": labels},
            "spec": {"unschedulable": in_use},
            "status": {
                "allocatable": node["allocatable"],
                "capacity": node["allocatable"],
                "conditions": [{"type": "Ready", "status": "True", "reason": "MockReady", "message": "fixture node"}],
            },
        })
        # 约 100 个已绑定 Pod 提供真实的 requestedResources 聚合输入；
        # 它们只占少量资源，不改变每 Core 1000 个可用 Node 的固定结论。
        if number % 30 == 1:
            kubernetes_pods.append({
                "apiVersion": "v1", "kind": "Pod",
                "metadata": {"name": f"bound-load-{number:04d}", "namespace": "ngd-ngg-test"},
                "spec": {
                    "nodeName": node_name,
                    "containers": [{
                        "name": "load", "image": "fixture.invalid/load:never-run",
                        "resources": {"requests": {"cpu": "1", "memory": "1Gi"}},
                    }],
                },
                "status": {"phase": "Running"},
            })

    static = {"clusterId": config["clusterId"], "topologyVersion": "dc-core-border-leaf-v1", "nodes": nodes}
    snapshot_id = canonical_hash(static)
    request_config = config["request"]
    request = {
        "requestId": "group1-3000-request",
        "taskUID": "task-group1-3000",
        "ngdUID": "ngd-group1-3000",
        "ngdGeneration": 1,
        "nodeStaticSnapshotId": snapshot_id,
        "podSets": [{
            "name": "distributed-workers",
            "replicas": request_config["replicas"],
            "minAvailable": request_config["minAvailable"],
            "resourcesPerPod": {"cpu": "1", "memory": "1Gi"},
        }],
        "nodeRequirements": {"nodeSelector": {"tests.ngg.io/worker": "true"}},
        "nodeUsageStates": states,
        "algorithms": [
            {"name": "requirement", "version": "v1", "parameters": {"requiredDistinctNodes": request_config["requiredDistinctNodes"]}},
            {"name": "topology", "version": "v1", "parameters": {"profile": "leaf-border-core-v1", "strategy": "NarrowestFit", "widestAllowedLevel": request_config["widestAllowedLevel"], "requiredDistinctNodes": request_config["requiredDistinctNodes"]}},
            {"name": "loadbalance", "version": "v1", "parameters": {"profile": "balanced-v2", "requireMetrics": True, "requireNetworkMetrics": True}},
        ],
        "maxCandidateGroups": request_config["maxCandidateGroups"],
    }
    return {
        "staticSnapshot": static,
        "snapshotId": snapshot_id,
        "dynamicState": {"scope": "request", "nodes": states},
        "metrics": metrics,
        "allocationRequest": request,
        "kubernetesNodes": kubernetes_nodes,
        "kubernetesPods": kubernetes_pods,
        "summary": {
            "nodeCount": len(nodes),
            "inUseCount": sum(1 for item in states if item["inUse"]),
            "availableCount": sum(1 for item in states if not item["inUse"]),
            "metricCount": len(METRIC_NAMES),
            "metricSamples": len(metrics) * len(METRIC_NAMES),
            "boundPodCount": len(kubernetes_pods),
            "coreCounts": {core: sum(1 for node in nodes if node["topology"]["coreSwitchId"] == core) for core in ("core-01", "core-02")},
        },
    }


def write_fixture(output: Path, data: dict[str, Any], include: set[str] | None = None) -> None:
    """只写当前测试组真正消费或需要展示的Fixture文件。"""

    output.mkdir(parents=True, exist_ok=True)
    files = {
        "node-static-snapshot.json": {"snapshotId": data["snapshotId"], **data["staticSnapshot"]},
        "node-dynamic-state.json": data["dynamicState"],
        "prometheus-metrics.json": {"source": "mock-prometheus-http", "nodes": data["metrics"]},
        "allocation-request.json": data["allocationRequest"],
        "kubernetes-nodes.json": {"items": data["kubernetesNodes"]},
        "kubernetes-pods.json": {"items": data["kubernetesPods"]},
        "fixture-summary.json": data["summary"],
    }
    selected = include if include is not None else set(files)
    unknown = selected.difference(files)
    if unknown:
        raise ValueError(f"unknown fixture files: {sorted(unknown)}")
    for name, value in files.items():
        if name in selected:
            write_json(output / name, value)


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser(description="生成三组测试共用的确定性 3000 Node Fixture")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--config", type=Path)
    args = parser.parse_args()
    value = generate(load_config(args.config))
    write_fixture(args.output, value)
    print(json.dumps({"output": str(args.output), "snapshotId": value["snapshotId"], **value["summary"]}, ensure_ascii=False))
