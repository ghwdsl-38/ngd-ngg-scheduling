"""把静态 Node、任务动态状态和标签约束合并为可参与算法的 Node 视图。"""

from __future__ import annotations

from typing import Any

from ..context import AllocationContext
from ..errors import InvalidRequest
from ..quantity import parse_resources, subtract


class NodeViewBuilder:
    """构造 FILTER 阶段使用的任务级可用 Node 列表。"""

    TOPOLOGY_FIELDS = {
        "dataCenter": "dataCenterId",
        "room": "roomId",
        "borderDomain": "borderDomainId",
        "spineDomain": "spineDomainId",
        "leafDomain": "leafDomainId",
    }

    def build(
        self,
        context: AllocationContext,
    ) -> list[dict[str, Any]]:
        """依次排除占用节点、标签不匹配节点和单 Pod 资源不足节点。"""

        ngd = context.request.get("ngd", {})
        if not isinstance(ngd, dict):
            raise InvalidRequest("ngd must be an object")
        label_selector = ngd.get("nodeSelector", {})
        if not isinstance(label_selector, dict):
            raise InvalidRequest("nodeRequirements.labelSelector must be an object")
        match_labels = label_selector.get("matchLabels", {})
        expressions = label_selector.get("matchExpressions", [])
        if not isinstance(match_labels, dict) or not isinstance(expressions, list):
            raise InvalidRequest("labelSelector matchLabels/matchExpressions are malformed")
        topology_constraints = context.request.get("topologyConstraints", {})
        if not isinstance(topology_constraints, dict):
            raise InvalidRequest("topologyConstraints must be an object")
        unknown_topology_levels = sorted(
            set(topology_constraints) - set(self.TOPOLOGY_FIELDS)
        )
        if unknown_topology_levels:
            raise InvalidRequest(
                "topologyConstraints contains unsupported levels: "
                + ", ".join(unknown_topology_levels)
            )
        usage = {}
        for item in context.request.get("nodeUsageStates", []):
            if not isinstance(item, dict):
                raise InvalidRequest("nodeUsageStates contains a malformed entry")
            usage[str(item.get("nodeUID", ""))] = item
        result: list[dict[str, Any]] = []
        for node in context.static_snapshot.nodes:
            uid = str(node["nodeUID"])
            state = usage.get(uid, {})
            if bool(state.get("inUse", False)):
                continue
            labels = node.get("labels", {})
            if not all(labels.get(key) == value for key, value in match_labels.items()):
                continue
            if not self._matches_expressions(labels, expressions):
                continue
            if not self._matches_topology(
                node.get("topology", {}), topology_constraints
            ):
                continue
            capacity = parse_resources(node.get("allocatable", {}))
            requested_raw = state.get("requestedResources", {})
            if not isinstance(requested_raw, dict):
                raise InvalidRequest(
                    f"nodeUsageStates[{uid}].requestedResources must be an object"
                )
            available = subtract(capacity, parse_resources(requested_raw))
            # 静态快照在一次计算中只读；这里只增加动态可用资源字段，浅拷贝
            # 顶层字典即可，避免为每次请求深拷贝所有标签和拓扑子对象。
            view = dict(node)
            view["availableResources"] = available
            # 后续评分直接复用已解析容量，避免同一Node重复解析Quantity。
            view["_allocatableResources"] = capacity
            result.append(view)
        # 固定排序使相同输入始终产生相同候选组和同分顺序。
        result.sort(
            key=lambda item: (str(item["nodeName"]), str(item["nodeUID"]))
        )
        return result

    @classmethod
    def _matches_topology(
        cls,
        topology: dict[str, Any],
        constraints: dict[str, Any],
    ) -> bool:
        """过滤具体逻辑域；requiredSame由后续GROUP阶段限制分组宽度。"""

        if not isinstance(topology, dict):
            return False
        for level, raw_value in constraints.items():
            value = str(raw_value)
            if value == "requiredSame":
                continue
            field = cls.TOPOLOGY_FIELDS[level]
            if str(topology.get(field, "")) != value:
                return False
        return True

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
