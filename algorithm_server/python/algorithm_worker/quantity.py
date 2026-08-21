"""解析 Kubernetes Quantity，并验证 PodSet 能否装入一个候选节点组。"""

from __future__ import annotations

import copy
import heapq
import re
from decimal import Decimal
from typing import Any

from .errors import InvalidRequest


_QUANTITY = re.compile(r"^([0-9]+(?:\.[0-9]+)?)([A-Za-z]+)?$")
_BINARY = {
    "Ki": Decimal(1024),
    "Mi": Decimal(1024) ** 2,
    "Gi": Decimal(1024) ** 3,
    "Ti": Decimal(1024) ** 4,
}


def parse_resources(resources: dict[str, Any]) -> dict[str, int]:
    """把 CPU 转为毫核、内存转为字节、扩展资源转为整数。"""

    result: dict[str, int] = {}
    for name, raw in resources.items():
        value = str(raw)
        if name == "cpu":
            result[name] = (
                int(Decimal(value[:-1]))
                if value.endswith("m")
                else int(Decimal(value) * 1000)
            )
            continue
        match = _QUANTITY.match(value)
        if not match:
            raise InvalidRequest(f"unsupported Kubernetes quantity: {value}")
        number, suffix = match.groups()
        if not suffix:
            result[name] = int(Decimal(number))
        elif suffix in _BINARY:
            result[name] = int(Decimal(number) * _BINARY[suffix])
        elif "/" in name:
            result[name] = int(Decimal(number))
        else:
            raise InvalidRequest(
                f"unsupported Kubernetes quantity suffix: {suffix}"
            )
    return result


def fits(available: dict[str, int], request: dict[str, int]) -> bool:
    """判断一份剩余资源是否能容纳一个 Pod 请求。"""

    return all(available.get(name, 0) >= value for name, value in request.items())


def subtract(
    available: dict[str, int],
    request: dict[str, int],
) -> dict[str, int]:
    """返回扣除一次 Pod 请求后的新资源字典，不修改传入值。"""

    result = dict(available)
    for name, value in request.items():
        result[name] = max(0, result.get(name, 0) - value)
    return result


def pod_set_minimums(
    pod_sets: list[dict[str, Any]],
) -> list[tuple[str, int, dict[str, int]]]:
    """提取每个 PodSet 必须同时满足的 minAvailable 和单 Pod 资源。"""

    result: list[tuple[str, int, dict[str, int]]] = []
    for pod_set in pod_sets:
        name = str(pod_set.get("name", ""))
        count = int(pod_set.get("minAvailable", 0))
        resources = pod_set.get("resourcesPerPod", {})
        if not name or count < 1 or not isinstance(resources, dict) or not resources:
            raise InvalidRequest(
                "each podSet needs name, minAvailable and resourcesPerPod"
            )
        result.append((name, count, parse_resources(resources)))
    if not result:
        raise InvalidRequest("podSets must not be empty")
    return result


def can_place_minimums(
    nodes: list[dict[str, Any]],
    minimums: list[tuple[str, int, dict[str, int]]],
    required_distinct_nodes: int = 0,
) -> bool:
    """用确定性贪心装箱验证整个任务的最小副本是否能放入该组。"""

    if required_distinct_nodes and len(nodes) < required_distinct_nodes:
        return False
    remaining = {
        str(node["nodeUID"]): parse_resources(node.get("allocatable", {}))
        for node in nodes
    }
    ordered = sorted(
        minimums,
        key=lambda item: sum(item[2].values()),
        reverse=True,
    )
    # 每个 PodSet 构建一次确定性最大堆。旧实现每放一个副本都扫描
    # 全部 Node，3000 Node/1000 副本时为 O(P*N)；堆实现降为
    # O((N+P)logN)，并以 UID 作为同分稳定次关键字。
    for _name, count, request in ordered:
        heap = [
            (
                -sum(capacity.get(key, 0) for key in request),
                uid,
            )
            for uid, capacity in remaining.items()
            if fits(capacity, request)
        ]
        heapq.heapify(heap)
        for _ in range(count):
            if not heap:
                return False
            _negative_capacity, chosen = heapq.heappop(heap)
            remaining[chosen] = subtract(remaining[chosen], request)
            if fits(remaining[chosen], request):
                heapq.heappush(
                    heap,
                    (
                        -sum(
                            remaining[chosen].get(key, 0)
                            for key in request
                        ),
                        chosen,
                    ),
                )
    return True


def deep_copy_resources(value: dict[str, int]) -> dict[str, int]:
    """为调用者提供显式的资源字典深拷贝。"""

    return copy.deepcopy(value)
