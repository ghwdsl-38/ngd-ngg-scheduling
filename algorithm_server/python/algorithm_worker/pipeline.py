"""注册、校验并执行 FILTER→GROUP→SCORE 可配置算法流水线。"""

from __future__ import annotations

from typing import Any

from .algorithms.loadbalance import LoadBalanceAlgorithm
from .algorithms.requirement import RequirementAlgorithm
from .algorithms.topology import TopologyAlgorithm
from .context import AllocationContext
from .errors import (
    InvalidAlgorithmOrder,
    InvalidAlgorithmParameters,
    InvalidRequest,
    UnknownAlgorithm,
)
from .models import AlgorithmPlugin, AlgorithmStage


class PipelineRunner:
    """按请求编排算法插件，并稳定返回不超过上限的候选组。"""

    DEFAULT_ALGORITHMS = [
        {"name": "requirement", "version": "v1", "parameters": {}},
        {"name": "topology", "version": "v1", "parameters": {}},
        {"name": "loadbalance", "version": "v1", "parameters": {}},
    ]

    def __init__(
        self,
        plugins: list[AlgorithmPlugin] | None = None,
    ) -> None:
        # 允许测试或以后扩展时注入插件；生产默认注册内置三阶段算法。
        registered = plugins or [
            RequirementAlgorithm(),
            TopologyAlgorithm(),
            LoadBalanceAlgorithm(),
        ]
        self.registry = {
            (plugin.name, plugin.version): plugin
            for plugin in registered
        }

    def run(self, context: AllocationContext) -> list[dict[str, Any]]:
        """执行完整流水线，把每阶段返回字段写回同一个请求上下文。"""

        algorithms = (
            context.request.get("algorithms")
            or self.DEFAULT_ALGORITHMS
        )
        if not isinstance(algorithms, list):
            raise InvalidRequest("algorithms must be an array")

        previous_stage: AlgorithmStage | None = None
        seen_algorithms: set[tuple[str, str]] = set()
        seen_stages: set[AlgorithmStage] = set()

        # 不允许重复插件、阶段倒序或缺少必需阶段。
        for raw in algorithms:
            plugin, parameters = self._resolve_plugin(
                raw,
                context.request,
            )
            key = (plugin.name, plugin.version)
            if key in seen_algorithms:
                raise InvalidAlgorithmOrder(
                    f"algorithm {plugin.name}/{plugin.version} is duplicated",
                    request_id=str(context.request.get("requestId", "")),
                )
            if (
                previous_stage is not None
                and plugin.stage < previous_stage
            ):
                raise InvalidAlgorithmOrder(
                    "algorithm stages must follow FILTER -> GROUP -> SCORE",
                    request_id=str(context.request.get("requestId", "")),
                )

            plugin.validate_parameters(parameters)
            trace = None
            if context.request.get("debugTrace") is True:
                trace = {
                    "algorithm": plugin.name,
                    "version": plugin.version,
                    "stage": plugin.stage.name,
                    "input": self._trace_input(plugin.stage, context, parameters),
                }
            result = plugin.execute(context, parameters)
            for field_name, value in result.items():
                setattr(context, field_name, value)
            if trace is not None:
                trace["output"] = self._trace_output(plugin.stage, context)
                context.pipeline_trace.append(trace)

            previous_stage = plugin.stage
            seen_algorithms.add(key)
            seen_stages.add(plugin.stage)

        required_stages = {
            AlgorithmStage.FILTER,
            AlgorithmStage.GROUP,
            AlgorithmStage.SCORE,
        }
        if seen_stages != required_stages:
            raise InvalidAlgorithmOrder(
                "pipeline must contain FILTER, GROUP and SCORE stages",
                request_id=str(context.request.get("requestId", "")),
            )

        max_groups = int(
            context.request.get("maxCandidateGroups", 3)
        )
        # SCORE 已稳定排序；这里只截断并生成连续 rank，不重新计算分数。
        context.candidates = context.candidates[:max_groups]
        for rank, group in enumerate(context.candidates, start=1):
            group["rank"] = rank
        return context.candidates

    @staticmethod
    def _node_refs(nodes: list[dict[str, Any]]) -> list[dict[str, str]]:
        """Trace 仅保留 Node 身份，完整资源/拓扑输入仍以静态快照文件为准。"""

        return [
            {"nodeUID": str(node.get("nodeUID", "")), "nodeName": str(node.get("nodeName", ""))}
            for node in nodes
        ]

    def _group_refs(self, groups: list[dict[str, Any]], include_scores: bool = False) -> list[dict[str, Any]]:
        result: list[dict[str, Any]] = []
        for group in groups:
            if include_scores:
                nodes = [
                    {
                        "nodeUID": str(node.get("nodeUID", "")),
                        "nodeName": str(node.get("nodeName", "")),
                        "score": node.get("score"),
                    }
                    for node in group.get("nodes", [])
                ]
            else:
                nodes = self._node_refs(group.get("nodes", []))
            item = {
                "groupId": group.get("groupId"),
                "topologyLevel": group.get("topologyLevel"),
                "nodeCount": len(nodes),
                "nodes": nodes,
            }
            if "groupScore" in group:
                item["groupScore"] = group["groupScore"]
            result.append(item)
        return result

    def _trace_input(
        self,
        stage: AlgorithmStage,
        context: AllocationContext,
        parameters: dict[str, Any],
    ) -> dict[str, Any]:
        common = {"parameters": parameters}
        if stage == AlgorithmStage.FILTER:
            states = context.request.get("nodeUsageStates") or []
            common.update({
                "staticSnapshotId": context.static_snapshot.snapshot_id,
                "staticNodeCount": len(context.static_snapshot.nodes),
                "nodeRequirements": context.request.get("nodeRequirements", {}),
                "nodeUsageStateCount": len(states),
                "nodeUsageStates": states,
                "podMinimums": context.pod_minimums,
            })
        elif stage == AlgorithmStage.GROUP:
            common.update({
                "availableNodeCount": len(context.current_nodes),
                "availableNodes": self._node_refs(context.current_nodes),
                "requiredDistinctNodes": context.required_distinct_nodes,
                "podMinimums": context.pod_minimums,
            })
        elif stage == AlgorithmStage.SCORE:
            metric_nodes = context.metric_snapshot.nodes if context.metric_snapshot else {}
            common.update({
                "groups": self._group_refs(context.node_groups),
                "metricSnapshotId": context.metric_snapshot.snapshot_id if context.metric_snapshot else "",
                "metricNodeCount": len(metric_nodes),
                "metricNames": sorted({name for values in metric_nodes.values() for name in values}),
                "metricsDegraded": context.metrics_degraded,
            })
        return common

    def _trace_output(self, stage: AlgorithmStage, context: AllocationContext) -> dict[str, Any]:
        if stage == AlgorithmStage.FILTER:
            return {
                "availableNodeCount": len(context.current_nodes),
                "filteredNodeCount": len(context.static_snapshot.nodes) - len(context.current_nodes),
                "availableNodes": self._node_refs(context.current_nodes),
                "requiredDistinctNodes": context.required_distinct_nodes,
            }
        if stage == AlgorithmStage.GROUP:
            return {"groupCount": len(context.node_groups), "groups": self._group_refs(context.node_groups)}
        return {"candidateGroupCount": len(context.candidates), "candidateNodeGroups": self._group_refs(context.candidates, include_scores=True)}

    def _resolve_plugin(
        self,
        raw: Any,
        request: dict[str, Any],
    ) -> tuple[AlgorithmPlugin, dict[str, Any]]:
        if not isinstance(raw, dict):
            raise InvalidRequest(
                "every algorithm entry must be an object"
            )
        key = (
            str(raw.get("name", "")),
            str(raw.get("version", "")),
        )
        plugin = self.registry.get(key)
        if plugin is None:
            raise UnknownAlgorithm(
                f"algorithm {key[0]}/{key[1]} is not registered",
                request_id=str(request.get("requestId", "")),
            )
        parameters = raw.get("parameters") or {}
        if not isinstance(parameters, dict):
            raise InvalidAlgorithmParameters(
                f"parameters for {key[0]}/{key[1]} must be an object",
                request_id=str(request.get("requestId", "")),
            )
        return plugin, parameters
