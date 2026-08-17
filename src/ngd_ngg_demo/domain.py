from __future__ import annotations

import copy
import re
from decimal import Decimal
from typing import Any


_QUANTITY = re.compile(r"^([0-9]+(?:\.[0-9]+)?)([A-Za-z]+)?$")
_BINARY = {
    "Ki": Decimal(1024),
    "Mi": Decimal(1024) ** 2,
    "Gi": Decimal(1024) ** 3,
    "Ti": Decimal(1024) ** 4,
}


def parse_cpu(value: str) -> int:
    if value.endswith("m"):
        return int(Decimal(value[:-1]))
    return int(Decimal(value) * 1000)


def parse_scalar(value: str) -> int:
    match = _QUANTITY.match(value)
    if not match:
        raise ValueError(f"unsupported Kubernetes quantity: {value}")
    number, suffix = match.groups()
    if not suffix:
        return int(Decimal(number))
    if suffix not in _BINARY:
        raise ValueError(f"unsupported Kubernetes quantity suffix: {suffix}")
    return int(Decimal(number) * _BINARY[suffix])


def parse_resources(resources: dict[str, str]) -> dict[str, int]:
    result: dict[str, int] = {}
    for name, raw in resources.items():
        if name == "cpu":
            result[name] = parse_cpu(str(raw))
        elif name == "memory":
            result[name] = parse_scalar(str(raw))
        elif "/" in name:
            result[name] = int(raw)
    return result


def add_resources(target: dict[str, int], delta: dict[str, int]) -> None:
    for name, value in delta.items():
        target[name] = target.get(name, 0) + value


def subtract_resources(left: dict[str, int], right: dict[str, int]) -> dict[str, int]:
    result = dict(left)
    for name, value in right.items():
        result[name] = max(0, result.get(name, 0) - value)
    return result


def fits(available: dict[str, int], request: dict[str, int]) -> bool:
    return all(available.get(name, 0) >= value for name, value in request.items())


def pod_requests(pod: dict[str, Any]) -> dict[str, int]:
    total: dict[str, int] = {}
    for container in pod.get("spec", {}).get("containers", []):
        raw = container.get("resources", {}).get("requests", {})
        add_resources(total, parse_resources(raw))
    return total


def node_is_ready(node: dict[str, Any]) -> bool:
    if node.get("spec", {}).get("unschedulable"):
        return False
    return any(
        condition.get("type") == "Ready" and condition.get("status") == "True"
        for condition in node.get("status", {}).get("conditions", [])
    )


def labels_match(node: dict[str, Any], selector: dict[str, str]) -> bool:
    labels = node.get("metadata", {}).get("labels", {})
    return all(labels.get(key) == value for key, value in selector.items())


def free_resources_by_node(
    nodes: list[dict[str, Any]], pods: list[dict[str, Any]]
) -> dict[str, dict[str, int]]:
    result = {
        node["metadata"]["name"]: parse_resources(
            node.get("status", {}).get("allocatable", {})
        )
        for node in nodes
    }
    used: dict[str, dict[str, int]] = {}
    for pod in pods:
        if pod.get("status", {}).get("phase") in ("Succeeded", "Failed"):
            continue
        node_name = pod.get("spec", {}).get("nodeName")
        if not node_name or node_name not in result:
            continue
        add_resources(used.setdefault(node_name, {}), pod_requests(pod))
    return {
        name: subtract_resources(resources, used.get(name, {}))
        for name, resources in result.items()
    }


def demand_minimums(demand_spec: dict[str, Any]) -> list[tuple[str, int, dict[str, int]]]:
    minimums = []
    for pod_set in demand_spec.get("podSets", []):
        minimums.append(
            (
                pod_set["name"],
                int(pod_set["minAvailable"]),
                parse_resources(pod_set["resourcesPerPod"]),
            )
        )
    return minimums


def can_place_minimums(
    candidate_names: list[str],
    free: dict[str, dict[str, int]],
    minimums: list[tuple[str, int, dict[str, int]]],
) -> bool:
    remaining = {name: copy.deepcopy(free[name]) for name in candidate_names}
    # Larger requests are placed first to reduce obvious fragmentation errors.
    ordered = sorted(
        minimums,
        key=lambda item: sum(item[2].values()),
        reverse=True,
    )
    for _pod_set_name, count, request in ordered:
        for _ in range(count):
            feasible = [name for name, capacity in remaining.items() if fits(capacity, request)]
            if not feasible:
                return False
            chosen = max(
                feasible,
                key=lambda name: sum(remaining[name].get(key, 0) for key in request),
            )
            remaining[chosen] = subtract_resources(remaining[chosen], request)
    return True


def node_score(node: dict[str, Any], free: dict[str, int]) -> int:
    allocatable = parse_resources(node.get("status", {}).get("allocatable", {}))
    ratios = []
    for name in ("cpu", "memory"):
        capacity = allocatable.get(name, 0)
        if capacity > 0:
            ratios.append(min(1.0, max(0.0, free.get(name, 0) / capacity)))
    if not ratios:
        return 0
    return round(100 * sum(ratios) / len(ratios))


def select_candidate_nodes(
    nodes: list[dict[str, Any]],
    pods: list[dict[str, Any]],
    demand_spec: dict[str, Any],
) -> tuple[list[dict[str, Any]], bool]:
    selector = demand_spec.get("nodeRequirements", {}).get("nodeSelector", {})
    free = free_resources_by_node(nodes, pods)
    minimums = demand_minimums(demand_spec)
    eligible = []
    for node in nodes:
        name = node["metadata"]["name"]
        if not node_is_ready(node) or not labels_match(node, selector):
            continue
        if not any(fits(free[name], request) for _, _, request in minimums):
            continue
        eligible.append(
            {
                "name": name,
                "uid": str(node["metadata"].get("uid", "")),
                "score": node_score(node, free[name]),
            }
        )
    eligible.sort(key=lambda item: (-item["score"], item["name"]))
    max_nodes = int(demand_spec.get("candidatePolicy", {}).get("maxNodes", 8))
    candidates = eligible[:max_nodes]
    satisfied = bool(candidates) and can_place_minimums(
        [item["name"] for item in candidates], free, minimums
    )
    return candidates if satisfied else [], satisfied


def stable_grant_spec(spec: dict[str, Any]) -> dict[str, Any]:
    return {
        key: copy.deepcopy(spec.get(key))
        for key in ("demandRef", "taskRef", "schedulerName", "source", "nodes")
    }

