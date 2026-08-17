from __future__ import annotations

import copy
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
    return all(available.get(name, 0) >= value for name, value in request.items())


def subtract(
    available: dict[str, int],
    request: dict[str, int],
) -> dict[str, int]:
    result = dict(available)
    for name, value in request.items():
        result[name] = max(0, result.get(name, 0) - value)
    return result


def pod_set_minimums(
    pod_sets: list[dict[str, Any]],
) -> list[tuple[str, int, dict[str, int]]]:
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
    for _name, count, request in ordered:
        for _ in range(count):
            feasible = [
                uid for uid, capacity in remaining.items() if fits(capacity, request)
            ]
            if not feasible:
                return False
            chosen = max(
                feasible,
                key=lambda uid: (
                    sum(remaining[uid].get(key, 0) for key in request),
                    uid,
                ),
            )
            remaining[chosen] = subtract(remaining[chosen], request)
    return True


def deep_copy_resources(value: dict[str, int]) -> dict[str, int]:
    return copy.deepcopy(value)
