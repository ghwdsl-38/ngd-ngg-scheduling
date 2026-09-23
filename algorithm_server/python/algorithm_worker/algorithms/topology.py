"""Topology GROUP stage over domains resolved by the Go topology package."""

from __future__ import annotations

from typing import Any

from ..context import AllocationContext
from ..errors import InvalidAlgorithmParameters
from ..models import AlgorithmStage


class TopologyAlgorithm:
    name = "topology"
    version = "v1"
    stage = AlgorithmStage.GROUP

    LAYERED_LEVELS = [
        {"name": "leafDomain", "field": "leafDomainId", "groupPrefix": "leaf-domain"},
        {"name": "spineDomain", "field": "spineDomainId", "groupPrefix": "spine-domain"},
        {"name": "borderDomain", "field": "borderDomainId", "groupPrefix": "border-domain"},
        {"name": "room", "field": "roomId", "groupPrefix": "room"},
        {"name": "dataCenter", "field": "dataCenterId", "groupPrefix": "data-center"},
    ]
    COMPATIBLE_LEVELS = [
        {"name": "leafDomain", "field": "leafDomainId", "groupPrefix": "leaf-domain"},
        {"name": "uplinkDomain", "field": "uplinkDomainId", "groupPrefix": "uplink-domain"},
        {"name": "room", "field": "roomId", "groupPrefix": "room"},
        {"name": "dataCenter", "field": "dataCenterId", "groupPrefix": "data-center"},
    ]

    def validate_parameters(self, parameters: dict[str, Any]) -> None:
        if parameters:
            raise InvalidAlgorithmParameters("fixed topology algorithm does not accept parameters")

    def execute(self, context: AllocationContext, parameters: dict[str, Any]) -> dict[str, Any]:
        constraints = context.request.get("topologyConstraints", {})
        if not isinstance(constraints, dict):
            raise InvalidAlgorithmParameters("topologyConstraints must be an object")
        mode = str(context.request.get("topologyMode", "layered"))
        levels = self.COMPATIBLE_LEVELS if mode == "uplink-compatible" else self.LAYERED_LEVELS
        order = {str(level["name"]): index for index, level in enumerate(levels)}
        if mode == "uplink-compatible":
            order.update({"spineDomain": 1, "borderDomain": 1})
        unknown = sorted(set(constraints) - set(order))
        if unknown:
            raise InvalidAlgorithmParameters("unsupported topology constraint levels: " + ", ".join(unknown))
        max_order = min((order[level] for level in constraints), default=len(levels) - 1)
        groups: list[dict[str, Any]] = []
        for index, level in enumerate(levels):
            if index > max_order:
                break
            groups.extend(self._group(context.current_nodes, str(level["field"]), str(level["name"]), str(level["groupPrefix"]), index))
        return {"node_groups": groups}

    @staticmethod
    def _group(nodes: list[dict[str, Any]], field: str, level: str, prefix: str, topology_order: int) -> list[dict[str, Any]]:
        grouped: dict[str, list[dict[str, Any]]] = {}
        for node in nodes:
            group_id = str(node.get("topology", {}).get(field, ""))
            if group_id:
                grouped.setdefault(group_id, []).append(node)
        result: list[dict[str, Any]] = []
        for raw_id in sorted(grouped):
            group_nodes = sorted(grouped[raw_id], key=lambda item: (str(item["nodeName"]), str(item["nodeUID"])))
            result.append({"groupId": f"{prefix}:{raw_id}", "topologyLevel": level, "topologyOrder": topology_order, "nodes": group_nodes})
        return result