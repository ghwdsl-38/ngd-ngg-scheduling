"""固定流水线 GROUP 阶段：按联通 NGD 的 Leaf→Border→Core 约束分组。"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from ..context import AllocationContext
from ..errors import InvalidAlgorithmParameters
from ..models import AlgorithmStage
from ..quantity import can_place_minimums


class TopologyAlgorithm:
    """实现 NarrowestFit：使用能够容纳整个任务的最窄拓扑层级。"""

    name = "topology"
    version = "v1"
    stage = AlgorithmStage.GROUP

    def __init__(self, profiles_path: Path | None = None) -> None:
        path = profiles_path or (
            Path(__file__).resolve().parents[1]
            / "config"
            / "topology_profiles.json"
        )
        with path.open(encoding="utf-8") as stream:
            self.profiles = json.load(stream)

    def validate_parameters(self, parameters: dict[str, Any]) -> None:
        allowed = {
            "profile",
            "strategy",
            "widestAllowedLevel",
        }
        unknown = sorted(set(parameters) - allowed)
        if unknown:
            raise InvalidAlgorithmParameters(
                f"topology/v1 unknown parameters: {', '.join(unknown)}"
            )
        profile_name = str(parameters.get("profile", "leaf-border-core-v1"))
        profile = self.profiles.get(profile_name)
        if not isinstance(profile, dict):
            raise InvalidAlgorithmParameters(
                f"topology/v1 profile {profile_name!r} is not configured"
            )
        if parameters.get("strategy", profile.get("strategy")) != "NarrowestFit":
            raise InvalidAlgorithmParameters(
                "topology/v1 strategy must be NarrowestFit"
            )
        level_names = {str(item["name"]) for item in profile.get("levels", [])}
        widest = str(parameters.get("widestAllowedLevel", "coreSwitch"))
        if widest not in level_names:
            raise InvalidAlgorithmParameters(
                f"widestAllowedLevel {widest!r} is not in topology profile"
            )

    def execute(
        self,
        context: AllocationContext,
        parameters: dict[str, Any],
    ) -> dict[str, Any]:
        resource_pool = context.request.get("requestMode") == "resourcePool"
        ngd = context.request.get("ngd", {}) if resource_pool else {}
        if resource_pool and not ngd.get("topologyRequirement"):
            nodes = sorted(
                context.current_nodes,
                key=lambda item: (str(item["nodeName"]), str(item["nodeUID"])),
            )
            return {"node_groups": ([{
                "groupId": "cluster:" + context.static_snapshot.cluster_id,
                "topologyLevel": "cluster",
                "topologyOrder": 0,
                "nodes": nodes,
            }] if nodes else [])}

        profile = self.profiles[str(parameters.get("profile", "leaf-border-core-v1"))]
        levels = profile["levels"]
        widest = str(parameters.get("widestAllowedLevel", "coreSwitch"))

        # 资源池模式先生成允许范围内全部层级；评分后再剔除不满足资源需求的
        # 组，并只保留仍可行的最窄层级。这样不会因 Leaf 资源不足而错过 Border。
        all_groups: list[dict[str, Any]] = []
        for order, level in enumerate(levels):
            groups = self._group(
                context.current_nodes,
                str(level["field"]),
                str(level["name"]),
                str(level["groupPrefix"]),
                order,
                context.pod_minimums,
            )
            if resource_pool:
                all_groups.extend(groups)
            elif groups:
                return {"node_groups": groups}
            if str(level["name"]) == widest:
                break
        return {"node_groups": all_groups if resource_pool else []}

    @staticmethod
    def _group(
        nodes: list[dict[str, Any]],
        field: str,
        level: str,
        prefix: str,
        topology_order: int,
        minimums: list[tuple[str, int, dict[str, int]]],
    ) -> list[dict[str, Any]]:
        grouped: dict[str, list[dict[str, Any]]] = {}
        for node in nodes:
            group_id = str(node.get("topology", {}).get(field, ""))
            if group_id:
                grouped.setdefault(group_id, []).append(node)

        result: list[dict[str, Any]] = []
        for raw_id in sorted(grouped):
            group_nodes = sorted(
                grouped[raw_id],
                key=lambda item: (str(item["nodeName"]), str(item["nodeUID"])),
            )
            if not minimums or can_place_minimums(group_nodes, minimums):
                result.append(
                    {
                        "groupId": f"{prefix}:{raw_id}",
                        "topologyLevel": level,
                        "topologyOrder": topology_order,
                        "nodes": group_nodes,
                    }
                )
        return result
