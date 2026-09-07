"""固定流水线 GROUP 阶段：按 Algorithm Server 已解析的拓扑逐层分组。"""

from __future__ import annotations

from typing import Any

from ..context import AllocationContext
from ..errors import InvalidAlgorithmParameters
from ..models import AlgorithmStage


class TopologyAlgorithm:
    """执行固定 NarrowestFit，不从 NGD 读取 profile 或拓扑图。"""

    name = "topology"
    version = "v1"
    stage = AlgorithmStage.GROUP

    # NGD拓扑约束范围固定为DataCenter到Leaf。order越小，拓扑越窄。
    # SPINE为空时Go层会把Spine约束转换为Border requiredSame；没有显式
    # Spine约束时，这一空层也只会自然跳过。
    LEVELS = [
        {
            "name": "leafDomain",
            "field": "leafDomainId",
            "groupPrefix": "leaf-domain",
        },
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
    ]

    ORDER_BY_CONSTRAINT = {
        "leafDomain": 0,
        "spineDomain": 1,
        "borderDomain": 2,
        "room": 3,
        "dataCenter": 4,
    }

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
        # 资源池模式先生成各层候选；评分阶段会从最窄层开始，找到首个可满足
        # NGD 资源需求的层级后停止。这样 Leaf 不够时可以上升到 Border Domain。
        constraints = context.request.get("topologyConstraints", {})
        if not isinstance(constraints, dict):
            raise InvalidAlgorithmParameters("topologyConstraints must be an object")
        unknown = sorted(set(constraints) - set(self.ORDER_BY_CONSTRAINT))
        if unknown:
            raise InvalidAlgorithmParameters(
                "unsupported topology constraint levels: " + ", ".join(unknown)
            )
        # 最深的显式条件决定候选组允许扩展到的最宽层级。例如Border
        # requiredSame允许Leaf/Spine/Border，但不允许继续扩大到Room。
        max_order = min(
            (self.ORDER_BY_CONSTRAINT[level] for level in constraints),
            default=4,
        )
        all_groups: list[dict[str, Any]] = []
        for order, level in enumerate(self.LEVELS):
            if order > max_order:
                break
            groups = self._group(
                context.current_nodes,
                str(level["field"]),
                str(level["name"]),
                str(level["groupPrefix"]),
                order,
            )
            all_groups.extend(groups)
        return {"node_groups": all_groups}

    @staticmethod
    def _group(
        nodes: list[dict[str, Any]],
        field: str,
        level: str,
        prefix: str,
        topology_order: int,
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
            result.append(
                {
                    "groupId": f"{prefix}:{raw_id}",
                    "topologyLevel": level,
                    "topologyOrder": topology_order,
                    "nodes": group_nodes,
                }
            )
        return result
