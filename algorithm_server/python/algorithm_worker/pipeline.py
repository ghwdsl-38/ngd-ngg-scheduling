"""以固定顺序执行 requirement→topology→loadbalance 算法流水线。"""

from __future__ import annotations

from typing import Any

from .algorithms.loadbalance import LoadBalanceAlgorithm
from .algorithms.requirement import RequirementAlgorithm
from .algorithms.topology import TopologyAlgorithm
from .context import AllocationContext
from .errors import InvalidRequest
from .models import AlgorithmStage


class PipelineRunner:
    """固定执行三步算法，并稳定返回不超过 NGD 上限的候选组。"""

    def __init__(self) -> None:
        self.requirement = RequirementAlgorithm()
        self.topology = TopologyAlgorithm()
        self.loadbalance = LoadBalanceAlgorithm()

    def run(self, context: AllocationContext) -> list[dict[str, Any]]:
        """执行完整流水线，把每阶段返回字段写回同一个请求上下文。"""

        plan = self._fixed_plan(context.request)
        for plugin, parameters in plan:
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

        if context.candidates:
            narrowest = min(int(group.get("topologyOrder", 0)) for group in context.candidates)
            context.candidates = [
                group for group in context.candidates
                if int(group.get("topologyOrder", 0)) == narrowest
            ]
            context.candidates.sort(key=lambda item: (-item["groupScore"], item["groupId"]))

        # 联通正式NGD没有候选组数量字段，资源池模式固定返回不超过3组。
        max_groups = 3
        # SCORE 已稳定排序；这里只截断、清理内部字段并生成连续 rank。
        context.candidates = context.candidates[:max_groups]
        for rank, group in enumerate(context.candidates, start=1):
            group.pop("topologyOrder", None)
            for node in group.get("nodes", []):
                node.pop("_availableResources", None)
            group["rank"] = rank
        return context.candidates

    def _fixed_plan(self, request: dict[str, Any]) -> list[tuple[Any, dict[str, Any]]]:
        """从固定服务配置和 NGD 约束构造参数，不读取 spec.algorithms。"""

        if request.get("requestMode") == "resourcePool" and not isinstance(
            request.get("ngd"), dict
        ):
            raise InvalidRequest("resourcePool request requires ngd")
        return [
            (self.requirement, {}),
            # 联通正式NGD不携带profile或拓扑图。层级和NarrowestFit策略
            # 由Algorithm Server固定实现，具体网络关系由Go层配置解析。
            (self.topology, {}),
            (self.loadbalance, {
                "profile": "balanced-v2",
                "requireMetrics": False,
                "requireNetworkMetrics": False,
            }),
        ]

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
                "requestedTopologyLabels": context.request.get("ngd", {}).get("topologyLabels", {}),
                "effectiveTopologyConstraints": context.request.get("topologyConstraints", {}),
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
            }
        if stage == AlgorithmStage.GROUP:
            return {"groupCount": len(context.node_groups), "groups": self._group_refs(context.node_groups)}
        return {"candidateGroupCount": len(context.candidates), "candidateNodeGroups": self._group_refs(context.candidates, include_scores=True)}
