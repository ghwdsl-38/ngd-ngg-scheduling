"""SCORE 阶段：综合资源、Prometheus 实时负载和静态拓扑质量稳定排序。"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from ..context import AllocationContext
from ..errors import InvalidAlgorithmParameters, RequiredMetricsNotReady
from ..models import AlgorithmStage


class LoadBalanceAlgorithm:
    """依据配置 profile 计算 Node score 和 groupScore。"""

    name = "loadbalance"
    version = "v1"
    stage = AlgorithmStage.SCORE

    NETWORK_METRICS = {
        "networkUtilizationRatio",
        "networkReceiveDropRatio",
        "networkTransmitDropRatio",
        "networkReceiveErrorRatio",
        "networkTransmitErrorRatio",
        "tcpRetransmitRatio",
        "networkLinkUpRatio",
        "availableBandwidthBytesPerSecond",
    }

    def __init__(self, profiles_path: Path | None = None) -> None:
        path = profiles_path or (
            Path(__file__).resolve().parents[1]
            / "config"
            / "loadbalance_profiles.json"
        )
        with path.open(encoding="utf-8") as stream:
            self.profiles = json.load(stream)

    def validate_parameters(self, parameters: dict[str, Any]) -> None:
        allowed = {"profile", "requireMetrics", "requireNetworkMetrics"}
        unknown = sorted(set(parameters) - allowed)
        if unknown:
            raise InvalidAlgorithmParameters(
                f"loadbalance/v1 unknown parameters: {', '.join(unknown)}"
            )
        profile = str(parameters.get("profile", "balanced-v2"))
        if profile not in self.profiles:
            raise InvalidAlgorithmParameters(
                f"loadbalance/v1 profile {profile!r} is not configured"
            )

    def execute(
        self,
        context: AllocationContext,
        parameters: dict[str, Any],
    ) -> dict[str, Any]:
        require_metrics = bool(parameters.get("requireMetrics", False))
        if require_metrics and (
            context.metric_snapshot is None or context.metrics_degraded
        ):
            raise RequiredMetricsNotReady(
                "Prometheus metrics required by loadbalance/v1 are not ready",
                request_id=str(context.request.get("requestId", "")),
            )

        profile = self.profiles[
            str(parameters.get("profile", "balanced-v2"))
        ]
        metrics_by_name = (
            context.metric_snapshot.nodes if context.metric_snapshot else {}
        )
        if bool(parameters.get("requireNetworkMetrics", False)):
            missing = [
                str(node["nodeName"])
                for group in context.node_groups
                for node in group["nodes"]
                if not self.NETWORK_METRICS.intersection(
                    metrics_by_name.get(str(node["nodeName"]), {})
                )
            ]
            if missing:
                raise RequiredMetricsNotReady(
                    "network metrics are missing for candidate Nodes: "
                    + ", ".join(sorted(set(missing))[:10]),
                    request_id=str(context.request.get("requestId", "")),
                )

        candidates: list[dict[str, Any]] = []
        for group in context.node_groups:
            nodes = [
                self._score_node(
                    node,
                    metrics_by_name.get(str(node["nodeName"])),
                    profile,
                )
                for node in group["nodes"]
            ]
            # 正式 NGG 要求 nodes 按 score 降序；同分时按名称和 UID 稳定排序。
            nodes.sort(
                key=lambda item: (
                    -item["score"], item["nodeName"], item["nodeUID"]
                )
            )
            average = sum(node["score"] for node in nodes) / len(nodes)
            topology_quality = self._topology_quality(group["nodes"])
            topology_weight = float(profile["topologyWeight"])
            score = round(
                (1.0 - topology_weight) * average
                + topology_weight * topology_quality,
                2,
            )
            candidates.append(
                {
                    "groupId": group["groupId"],
                    "topologyLevel": group["topologyLevel"],
                    "groupScore": score,
                    "topologyOrder": int(group.get("topologyOrder", 99)),
                    "nodes": nodes,
                }
            )

        candidates.sort(
            key=lambda item: (
                -item["groupScore"],
                item["topologyOrder"],
                item["groupId"],
            )
        )
        for item in candidates:
            item.pop("topologyOrder", None)
        return {"candidates": candidates}

    @staticmethod
    def _score_node(
        node: dict[str, Any],
        metrics: dict[str, float] | None,
        profile: dict[str, Any],
    ) -> dict[str, Any]:
        resource_score = 100.0 if node.get("allocatable") else 0.0
        weighted_score = 0.0
        available_weight = 0.0
        for name, rule in profile.get("metrics", {}).items():
            if not metrics or name not in metrics:
                continue
            weight = max(0.0, float(rule.get("weight", 0)))
            reference = max(1e-12, float(rule.get("reference", 1)))
            normalized = max(0.0, min(1.0, float(metrics[name]) / reference))
            if rule.get("direction", "lower") == "lower":
                normalized = 1.0 - normalized
            weighted_score += weight * normalized * 100.0
            available_weight += weight
        metric_score = weighted_score / available_weight if available_weight else 100.0
        resource_weight = float(profile["resourceWeight"])
        metric_weight = float(profile["metricWeight"])
        total_weight = resource_weight + metric_weight
        score = (
            resource_weight * resource_score + metric_weight * metric_score
        ) / total_weight if total_weight else 100.0
        return {
            "nodeUID": str(node["nodeUID"]),
            "nodeName": str(node["nodeName"]),
            "score": max(0, min(100, round(score))),
        }

    @staticmethod
    def _topology_quality(nodes: list[dict[str, Any]]) -> float:
        values: list[float] = []
        for node in nodes:
            topology = node.get("topology", {})
            bandwidth = max(0.0, float(topology.get("bandwidthGbps", 10)))
            latency = max(0.001, float(topology.get("latencyMillis", 5)))
            bandwidth_score = min(100.0, bandwidth / 25.0 * 100.0)
            latency_score = min(100.0, 1.0 / latency * 100.0)
            values.append(0.7 * bandwidth_score + 0.3 * latency_score)
        return sum(values) / len(values) if values else 0.0
