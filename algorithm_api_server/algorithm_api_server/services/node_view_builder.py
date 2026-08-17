from __future__ import annotations

import copy
from typing import Any

from ..context import AllocationContext
from ..errors import InvalidRequest
from ..quantity import fits, parse_resources


class NodeViewBuilder:
    """Builds the task-local eligible Node view used by FILTER algorithms."""

    def build(
        self,
        context: AllocationContext,
    ) -> list[dict[str, Any]]:
        selector = context.request.get("nodeRequirements", {}).get(
            "nodeSelector", {}
        )
        if not isinstance(selector, dict):
            raise InvalidRequest("nodeRequirements.nodeSelector must be an object")
        usage = {
            str(item.get("nodeUID", "")): bool(item.get("inUse", False))
            for item in context.request.get("nodeUsageStates", [])
        }
        result: list[dict[str, Any]] = []
        for node in context.static_snapshot.nodes:
            uid = str(node["nodeUID"])
            if usage.get(uid, False):
                continue
            labels = node.get("labels", {})
            if not all(labels.get(key) == value for key, value in selector.items()):
                continue
            capacity = parse_resources(node.get("allocatable", {}))
            if not any(
                fits(capacity, request)
                for _, _, request in context.pod_minimums
            ):
                continue
            result.append(copy.deepcopy(node))
        result.sort(
            key=lambda item: (str(item["nodeName"]), str(item["nodeUID"]))
        )
        return result
