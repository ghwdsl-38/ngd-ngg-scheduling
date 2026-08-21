"""把静态 Node、任务动态状态和标签约束合并为可参与算法的 Node 视图。"""

from __future__ import annotations

import copy
from typing import Any

from ..context import AllocationContext
from ..errors import InvalidRequest
from ..quantity import fits, parse_resources


class NodeViewBuilder:
    """构造 FILTER 阶段使用的任务级可用 Node 列表。"""

    def build(
        self,
        context: AllocationContext,
    ) -> list[dict[str, Any]]:
        """依次排除占用节点、标签不匹配节点和单 Pod 资源不足节点。"""

        requirements = context.request.get("nodeRequirements", {})
        selector = requirements.get("nodeSelector", {})
        if not isinstance(selector, dict):
            raise InvalidRequest("nodeRequirements.nodeSelector must be an object")
        label_selector = requirements.get("labelSelector", {})
        if not isinstance(label_selector, dict):
            raise InvalidRequest("nodeRequirements.labelSelector must be an object")
        match_labels = label_selector.get("matchLabels", {})
        expressions = label_selector.get("matchExpressions", [])
        if not isinstance(match_labels, dict) or not isinstance(expressions, list):
            raise InvalidRequest("labelSelector matchLabels/matchExpressions are malformed")
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
            if not all(labels.get(key) == value for key, value in match_labels.items()):
                continue
            if not self._matches_expressions(labels, expressions):
                continue
            capacity = parse_resources(node.get("allocatable", {}))
            if not any(
                fits(capacity, request)
                for _, _, request in context.pod_minimums
            ):
                continue
            result.append(copy.deepcopy(node))
        # 固定排序使相同输入始终产生相同候选组和同分顺序。
        result.sort(
            key=lambda item: (str(item["nodeName"]), str(item["nodeUID"]))
        )
        return result

    @staticmethod
    def _matches_expressions(
        labels: dict[str, str], expressions: list[dict[str, Any]]
    ) -> bool:
        """实现正式 CRD 声明的 In/NotIn/Exists/DoesNotExist 语义。"""

        for expression in expressions:
            if not isinstance(expression, dict):
                raise InvalidRequest("labelSelector contains a malformed expression")
            key = str(expression.get("key", ""))
            operator = str(expression.get("operator", ""))
            values = expression.get("values", [])
            if not key or not isinstance(values, list):
                raise InvalidRequest("labelSelector expression needs key and values")
            present = key in labels
            if operator == "In" and (not present or labels[key] not in values):
                return False
            if operator == "NotIn" and present and labels[key] in values:
                return False
            if operator == "Exists" and not present:
                return False
            if operator == "DoesNotExist" and present:
                return False
            if operator not in {"In", "NotIn", "Exists", "DoesNotExist"}:
                raise InvalidRequest(f"unsupported labelSelector operator: {operator}")
        return True
