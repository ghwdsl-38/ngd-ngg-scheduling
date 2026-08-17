from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from ..context import AllocationContext
from ..errors import (
    InvalidAlgorithmParameters,
    RequiredMetricsNotReady,
)
from ..models import AlgorithmStage


class LoadBalanceAlgorithm:
    name = "loadbalance"
    version = "v1"
    stage = AlgorithmStage.SCORE

    def __init__(self, profiles_path: Path | None = None) -> None:
        path = profiles_path or (
            Path(__file__).resolve().parents[1]
            / "config"
            / "loadbalance_profiles.json"
        )
        with path.open(encoding="utf-8") as stream:
            self.profiles = json.load(stream)

    def validate_parameters(self, parameters: dict[str, Any]) -> None:
        allowed = {"profile", "requireMetrics"}
        unknown = sorted(set(parameters) - allowed)
        if unknown:
            raise InvalidAlgorithmParameters(
                f"loadbalance/v1 unknown parameters: {', '.join(unknown)}"
            )
        profile = str(parameters.get("profile", "balanced-v1"))
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

        profile_name = str(parameters.get("profile", "balanced-v1"))
        profile = self.profiles[profile_name]
        metrics_by_name = (
            context.metric_snapshot.nodes if context.metric_snapshot else {}
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
                    "nodes": nodes,
                }
            )

        candidates.sort(
            key=lambda item: (
                -item["groupScore"],
                0 if item["topologyLevel"] == "leafSwitch" else 1,
                item["groupId"],
            )
        )
        return {"candidates": candidates}

    @staticmethod
    def _score_node(
        node: dict[str, Any],
        metrics: dict[str, float] | None,
        profile: dict[str, float],
    ) -> dict[str, Any]:
        resource_score = 100.0 if node.get("allocatable") else 0.0
        values = [
            metrics[key]
            for key in ("cpuUsageRatio", "memoryUsageRatio")
            if metrics and key in metrics
        ]
        load_score = (
            100.0 * (1.0 - sum(values) / len(values))
            if values
            else 100.0
        )
        resource_weight = float(profile["resourceWeight"])
        load_weight = float(profile["loadWeight"])
        total_weight = resource_weight + load_weight
        score = (
            resource_weight * resource_score
            + load_weight * load_score
        ) / total_weight
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
            bandwidth = max(
                0.0,
                float(topology.get("bandwidthGbps", 10)),
            )
            latency = max(
                0.001,
                float(topology.get("latencyMillis", 5)),
            )
            bandwidth_score = min(100.0, bandwidth / 25.0 * 100.0)
            latency_score = min(100.0, 1.0 / latency * 100.0)
            values.append(
                0.7 * bandwidth_score + 0.3 * latency_score
            )
        return sum(values) / len(values) if values else 0.0
