from __future__ import annotations

from typing import Any

from ..context import AllocationContext
from ..errors import InvalidAlgorithmParameters
from ..models import AlgorithmStage
from ..quantity import can_place_minimums


class TopologyAlgorithm:
    name = "topology"
    version = "v1"
    stage = AlgorithmStage.GROUP

    def validate_parameters(self, parameters: dict[str, Any]) -> None:
        allowed = {"strategy", "widestAllowedLevel", "requiredDistinctNodes"}
        unknown = sorted(set(parameters) - allowed)
        if unknown:
            raise InvalidAlgorithmParameters(
                f"topology/v1 unknown parameters: {', '.join(unknown)}"
            )
        if parameters.get("strategy", "NarrowestFit") != "NarrowestFit":
            raise InvalidAlgorithmParameters(
                "topology/v1 strategy must be NarrowestFit"
            )
        if parameters.get("widestAllowedLevel", "leafSwitch") not in {
            "leafSwitch",
            "coreSwitch",
        }:
            raise InvalidAlgorithmParameters(
                "widestAllowedLevel must be leafSwitch or coreSwitch"
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
        required_distinct = int(
            parameters.get(
                "requiredDistinctNodes",
                context.required_distinct_nodes,
            )
        )
        leaf_groups = self._group(
            context.current_nodes,
            "leafSwitchId",
            "leafSwitch",
            context.pod_minimums,
            required_distinct,
        )
        if leaf_groups:
            return {"node_groups": leaf_groups}
        if parameters.get("widestAllowedLevel", "leafSwitch") == "coreSwitch":
            return {
                "node_groups": self._group(
                    context.current_nodes,
                    "coreSwitchId",
                    "coreSwitch",
                    context.pod_minimums,
                    required_distinct,
                )
            }
        return {"node_groups": []}

    @staticmethod
    def _group(
        nodes: list[dict[str, Any]],
        field: str,
        level: str,
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
                key=lambda item: (
                    str(item["nodeName"]),
                    str(item["nodeUID"]),
                ),
            )
            if can_place_minimums(
                group_nodes,
                minimums,
                required_distinct,
            ):
                prefix = "leaf" if level == "leafSwitch" else "core"
                result.append(
                    {
                        "groupId": f"{prefix}:{raw_id}",
                        "topologyLevel": level,
                        "nodes": group_nodes,
                    }
                )
        return result
