"""GROUP 阶段：按照可配置的 Leaf→Border→Core 三层网络拓扑形成候选组。"""

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
            "requiredDistinctNodes",
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
        if int(parameters.get("requiredDistinctNodes", 0)) < 0:
            raise InvalidAlgorithmParameters(
                "requiredDistinctNodes must be zero or greater"
            )

    def execute(
        self,
        context: AllocationContext,
        parameters: dict[str, Any],
    ) -> dict[str, Any]:
        profile = self.profiles[
            str(parameters.get("profile", "leaf-border-core-v1"))
        ]
        levels = profile["levels"]
        widest = str(parameters.get("widestAllowedLevel", "coreSwitch"))
        required_distinct = int(
            parameters.get(
                "requiredDistinctNodes",
                context.required_distinct_nodes,
            )
        )

        # 依次尝试 Leaf、Border、Core；可选层级没有任何数据时自然跳过。
        for order, level in enumerate(levels):
            groups = self._group(
                context.current_nodes,
                str(level["field"]),
                str(level["name"]),
                str(level["groupPrefix"]),
                order,
                context.pod_minimums,
                required_distinct,
            )
            if groups:
                return {"node_groups": groups}
            if str(level["name"]) == widest:
                break
        return {"node_groups": []}

    @staticmethod
    def _group(
        nodes: list[dict[str, Any]],
        field: str,
        level: str,
        prefix: str,
        topology_order: int,
        minimums: list[tuple[str, int, dict[str, int]]],
        required_distinct: int,
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
            if can_place_minimums(group_nodes, minimums, required_distinct):
                result.append(
                    {
                        "groupId": f"{prefix}:{raw_id}",
                        "topologyLevel": level,
                        "topologyOrder": topology_order,
                        "nodes": group_nodes,
                    }
                )
        return result
