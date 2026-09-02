"""固定流水线 GROUP 阶段：按 Algorithm Server 已解析的拓扑逐层分组。"""

from __future__ import annotations

from typing import Any

from ..context import AllocationContext
from ..errors import InvalidAlgorithmParameters
from ..models import AlgorithmStage
from ..quantity import can_place_minimums


class TopologyAlgorithm:
    """执行固定 NarrowestFit，不从 NGD 读取 profile 或拓扑图。"""

    name = "topology"
    version = "v1"
    stage = AlgorithmStage.GROUP

    # SPINE 可以为空；为空时该层不会产生候选组，算法自然继续到 Border Domain。
    LEVELS = [
        {"name": "leafSwitch", "field": "leafSwitchId", "groupPrefix": "leaf"},
        {
            "name": "spineDomain",
            "field": "spineDomainId",
            "groupPrefix": "spine-domain",
        },
        {
            "name": "borderDomain",
            "field": "borderDomainId",
            "groupPrefix": "border-domain",
        },
        {"name": "room", "field": "roomId", "groupPrefix": "room"},
        {
            "name": "dataCenter",
            "field": "dataCenterId",
            "groupPrefix": "data-center",
        },
        {"name": "location", "field": "locationId", "groupPrefix": "location"},
        {"name": "region", "field": "regionId", "groupPrefix": "region"},
    ]

    def validate_parameters(self, parameters: dict[str, Any]) -> None:
        if parameters:
            raise InvalidAlgorithmParameters(
                "fixed topology algorithm does not accept parameters"
            )

    def execute(
        self,
        context: AllocationContext,
        parameters: dict[str, Any],
    ) -> dict[str, Any]:
        resource_pool = context.request.get("requestMode") == "resourcePool"

        # 资源池模式先生成各层候选；评分阶段会从最窄层开始，找到首个可满足
        # NGD 资源需求的层级后停止。这样 Leaf 不够时可以上升到 Border Domain。
        all_groups: list[dict[str, Any]] = []
        for order, level in enumerate(self.LEVELS):
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
