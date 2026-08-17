from __future__ import annotations

import hashlib
import json
from datetime import datetime, timezone
from typing import Any

from .domain import add_resources, node_is_ready, pod_requests


def canonical_hash(value: Any) -> str:
    encoded = json.dumps(
        value,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return "sha256:" + hashlib.sha256(encoded).hexdigest()


def build_static_snapshot(
    cluster_id: str,
    nodes: list[dict[str, Any]],
    topologies: list[dict[str, Any]],
) -> tuple[str, dict[str, Any]]:
    topology_by_uid: dict[str, dict[str, Any]] = {}
    versions: set[str] = set()
    for item in topologies:
        spec = item.get("spec", {})
        status = item.get("status", {})
        uid = str(spec.get("nodeRef", {}).get("uid", ""))
        if not uid:
            continue
        topology_by_uid[uid] = {
            "switchId": str(status.get("switchId", "")),
            "coreSwitchId": str(status.get("coreSwitchId", "")),
            "bandwidthGbps": float(status.get("bandwidthGbps", 0)),
            "latencyMillis": float(status.get("latencyMillis", 0)),
        }
        version = str(status.get("topologyVersion", ""))
        if version:
            versions.add(version)

    static_nodes: list[dict[str, Any]] = []
    for node in nodes:
        metadata = node.get("metadata", {})
        uid = str(metadata.get("uid", ""))
        topology = topology_by_uid.get(uid)
        if not topology or not topology["switchId"]:
            continue
        static_nodes.append(
            {
                "nodeName": str(metadata.get("name", "")),
                "nodeUID": uid,
                "createdAt": str(metadata.get("creationTimestamp", "")),
                "allocatable": node.get("status", {}).get("allocatable", {}),
                "labels": metadata.get("labels", {}),
                "topology": topology,
            }
        )
    static_nodes.sort(key=lambda item: (item["nodeUID"], item["nodeName"]))
    content = {
        "clusterId": cluster_id,
        "topologyVersion": "+".join(sorted(versions)) or "unknown",
        "nodes": static_nodes,
    }
    return canonical_hash(content), content


def build_scheduler_state(
    nodes: list[dict[str, Any]], pods: list[dict[str, Any]]
) -> tuple[str, str, list[dict[str, Any]]]:
    requested: dict[str, dict[str, int]] = {}
    for pod in pods:
        if pod.get("status", {}).get("phase") in ("Succeeded", "Failed"):
            continue
        node_name = str(pod.get("spec", {}).get("nodeName", ""))
        if not node_name:
            continue
        add_resources(requested.setdefault(node_name, {}), pod_requests(pod))

    state: list[dict[str, Any]] = []
    for node in nodes:
        name = str(node.get("metadata", {}).get("name", ""))
        state.append(
            {
                "nodeUID": str(node.get("metadata", {}).get("uid", "")),
                "ready": node_is_ready(node),
                "unschedulable": bool(node.get("spec", {}).get("unschedulable")),
                "requestedResources": format_resources(requested.get(name, {})),
            }
        )
    state.sort(key=lambda item: item["nodeUID"])
    snapshot_id = canonical_hash(state)
    captured_at = datetime.now(timezone.utc).isoformat(timespec="seconds").replace(
        "+00:00", "Z"
    )
    return snapshot_id, captured_at, state


def format_resources(resources: dict[str, int]) -> dict[str, str]:
    result: dict[str, str] = {}
    for name, value in resources.items():
        result[name] = f"{value}m" if name == "cpu" else str(value)
    return result

