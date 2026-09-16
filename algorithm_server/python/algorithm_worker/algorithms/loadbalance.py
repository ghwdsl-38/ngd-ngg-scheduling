"""SCORE 阶段：综合资源、Prometheus 实时负载和静态拓扑质量稳定排序。"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from ..context import AllocationContext
from ..errors import InvalidAlgorithmParameters, RequiredMetricsNotReady
from ..models import AlgorithmStage
from ..quantity import parse_normalized_resources


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

        # 一个Node可能同时出现在Leaf、Border、Core组中。先按UID去重，既用于
        # 指标完整性检查，也用于后续只计算一次Node分数。
        source_by_uid: dict[str, dict[str, Any]] = {}
        for group in context.node_groups:
            for node in group["nodes"]:
                source_by_uid.setdefault(str(node["nodeUID"]), node)
        if bool(parameters.get("requireNetworkMetrics", False)):
            missing = [
                str(node["nodeName"])
                for node in source_by_uid.values()
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

        # 资源和Prometheus分数只与Node本身有关，因此每次请求只计算一次，
        # 后续各层级直接复用。
        score_by_uid = {
            uid: self._score_node(
                node,
                metrics_by_name.get(str(node["nodeName"])),
                profile,
            )
            for uid, node in source_by_uid.items()
        }

        grouped_by_order: dict[int, list[dict[str, Any]]] = {}
        for group in context.node_groups:
            grouped_by_order.setdefault(
                int(group.get("topologyOrder", 99)), []
            ).append(group)

        candidates: list[dict[str, Any]] = []
        for topology_order in sorted(grouped_by_order):
            level_candidates: list[dict[str, Any]] = []
            for group in grouped_by_order[topology_order]:
                # selection会删除内部资源字段，所以每个组使用浅拷贝；缓存本身
                # 保持不变，可安全复用于更宽拓扑层级。
                nodes = [
                    dict(score_by_uid[str(node["nodeUID"])])
                    for node in group["nodes"]
                ]
                # 正式 NGG 要求 nodes 按 score 降序；同分时按名称和 UID 稳定排序。
                nodes.sort(
                    key=lambda item: (
                        -item["score"], item["nodeName"], item["nodeUID"]
                    )
                )
                nodes, diagnostic = self._select_resource_pool_nodes(context, nodes)
                diagnostic["groupId"] = group["groupId"]
                context.diagnostics.setdefault("resourceGroups", []).append(diagnostic)
                if not nodes:
                    continue
                average = sum(node["score"] for node in nodes) / len(nodes)
                selected_ids = {str(node["nodeUID"]) for node in nodes}
                selected_sources = [
                    node for node in group["nodes"]
                    if str(node["nodeUID"]) in selected_ids
                ]
                topology_quality = self._topology_quality(selected_sources)
                topology_weight = float(profile["topologyWeight"])
                score = round(
                    (1.0 - topology_weight) * average
                    + topology_weight * topology_quality,
                    2,
                )
                level_candidates.append(
                    {
                        "groupId": group["groupId"],
                        "topologyLevel": group["topologyLevel"],
                        "groupScore": score,
                        "topologyOrder": topology_order,
                        "nodes": nodes,
                    }
                )
            candidates.extend(level_candidates)
            # NarrowestFit在当前层有任一可行组后即停止，不再处理更宽层级。
            if level_candidates:
                break

        candidates.sort(
            key=lambda item: (
                -item["groupScore"],
                item["topologyOrder"],
                item["groupId"],
            )
        )
        return {"candidates": candidates}

    @staticmethod
    def _score_node(
        node: dict[str, Any],
        metrics: dict[str, float] | None,
        profile: dict[str, Any],
    ) -> dict[str, Any]:
        allocatable = node.get("_allocatableResources", {})
        if not isinstance(allocatable, dict):
            raise InvalidAlgorithmParameters("normalized allocatable resources are missing")
        available = node.get("availableResources", allocatable)
        resource_ratios = [
            min(1.0, max(0.0, available.get(name, 0) / capacity))
            for name, capacity in allocatable.items()
            if capacity > 0 and name in {"cpu", "memory"}
        ]
        resource_score = (
            sum(resource_ratios) / len(resource_ratios) * 100.0
            if resource_ratios else 0.0
        )
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
        result = {
            "nodeUID": str(node["nodeUID"]),
            "nodeName": str(node["nodeName"]),
            "score": max(0, min(100, round(score))),
            "_availableResources": dict(available),
            "topology": dict(node.get("topology", {})),
        }
        if "cpu" in available or "memory" in available:
            result["resources"] = {
                "cpuMilli": str(available.get("cpu", 0)),
                "memoryBytes": str(available.get("memory", 0)),
            }
        return result

    @staticmethod
    def _select_resource_pool_nodes(
        context: AllocationContext,
        ranked_nodes: list[dict[str, Any]],
    ) -> tuple[list[dict[str, Any]], dict[str, Any]]:
        ngd = context.request.get("ngd", {})
        normalized = ngd.get("normalizedResources", {})
        minimum = parse_normalized_resources(
            normalized.get("minResources", {})
        )
        quota = parse_normalized_resources(normalized.get("quota", {}))
        max_nodes = int(ngd.get("maxNodes", len(ranked_nodes)))

        selected: list[dict[str, Any]] = []
        totals: dict[str, int] = {}
        quota_rejected = 0
        for node in ranked_nodes:
            if len(selected) >= max_nodes:
                break
            available = node.get("_availableResources", {})
            proposed = {
                name: totals.get(name, 0) + int(value)
                for name, value in available.items()
            }
            if any(proposed.get(name, 0) > limit for name, limit in quota.items()):
                quota_rejected += 1
                continue
            selected.append(node)
            totals = proposed
            if minimum and LoadBalanceAlgorithm._satisfies(totals, minimum):
                break

        feasible = not minimum or LoadBalanceAlgorithm._satisfies(totals, minimum)
        diagnostic: dict[str, Any] = {
            "eligibleNodeCount": len(ranked_nodes),
            "selectedNodeCount": len(selected),
            "maxNodes": max_nodes,
            "quotaRejectedNodeCount": quota_rejected,
            "required": LoadBalanceAlgorithm._resource_strings(minimum),
            "quota": LoadBalanceAlgorithm._resource_strings(quota),
            "achievable": LoadBalanceAlgorithm._resource_strings(totals),
            "code": "",
        }
        if not feasible:
            without_quota = LoadBalanceAlgorithm._sum_resources(
                ranked_nodes[:max_nodes]
            )
            all_resources = LoadBalanceAlgorithm._sum_resources(ranked_nodes)
            diagnostic["achievable"] = LoadBalanceAlgorithm._resource_strings(
                without_quota
            )
            if quota and LoadBalanceAlgorithm._satisfies(without_quota, minimum):
                diagnostic["code"] = "QUOTA_PREVENTS_MINIMUM"
            elif (
                max_nodes < len(ranked_nodes)
                and LoadBalanceAlgorithm._satisfies(all_resources, minimum)
            ):
                diagnostic["code"] = "MAX_NODES_PREVENTS_MINIMUM"
            else:
                diagnostic["code"] = "INSUFFICIENT_RESOURCES"
            return [], diagnostic

        for node in selected:
            node.pop("_availableResources", None)
        return selected, diagnostic

    @staticmethod
    def _satisfies(values: dict[str, int], minimum: dict[str, int]) -> bool:
        return all(values.get(name, 0) >= value for name, value in minimum.items())

    @staticmethod
    def _sum_resources(nodes: list[dict[str, Any]]) -> dict[str, int]:
        totals: dict[str, int] = {}
        for node in nodes:
            for name, value in node.get("_availableResources", {}).items():
                totals[name] = totals.get(name, 0) + int(value)
        return totals

    @staticmethod
    def _resource_strings(values: dict[str, int]) -> dict[str, str]:
        return {name: str(value) for name, value in values.items()}

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
