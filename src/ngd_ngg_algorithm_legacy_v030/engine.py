from __future__ import annotations

import copy
import hashlib
import json
import threading
import uuid
from dataclasses import dataclass
from typing import Any

from .metrics import MetricsCache

from ngd_ngg_demo.domain import (
    can_place_minimums,
    demand_minimums,
    fits,
    parse_resources,
    subtract_resources,
)


def canonical_hash(value: Any) -> str:
    encoded = json.dumps(
        value,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return "sha256:" + hashlib.sha256(encoded).hexdigest()


@dataclass(frozen=True)
class StaticSnapshot:
    snapshot_id: str
    topology_version: str
    nodes: tuple[dict[str, Any], ...]


class AlgorithmError(ValueError):
    pass


class SnapshotNotReady(AlgorithmError):
    pass


class AlgorithmEngine:
    """Thread-safe in-memory snapshot cache and deterministic Top-3 algorithm."""

    def __init__(self, metrics_cache: MetricsCache | None = None) -> None:
        self.boot_id = f"boot-{uuid.uuid4().hex[:12]}"
        self._lock = threading.RLock()
        self._current: StaticSnapshot | None = None
        self._previous: StaticSnapshot | None = None
        self.metrics_cache = metrics_cache or MetricsCache()

    def put_static_snapshot(
        self, snapshot_id: str, body: dict[str, Any]
    ) -> dict[str, Any]:
        content = {
            "clusterId": str(body.get("clusterId", "")),
            "topologyVersion": str(body.get("topologyVersion", "")),
            "nodes": body.get("nodes", []),
        }
        actual = canonical_hash(content)
        if actual != snapshot_id:
            raise AlgorithmError(
                f"snapshot checksum mismatch: path={snapshot_id} actual={actual}"
            )
        nodes = self._validate_static_nodes(content["nodes"])
        snapshot = StaticSnapshot(
            snapshot_id=snapshot_id,
            topology_version=content["topologyVersion"],
            nodes=tuple(copy.deepcopy(nodes)),
        )
        with self._lock:
            if self._current and self._current.snapshot_id != snapshot_id:
                self._previous = self._current
            self._current = snapshot
        return {
            "algorithmBootId": self.boot_id,
            "acceptedSnapshotId": snapshot_id,
            "nodeCount": len(nodes),
            "checksum": actual,
        }

    def cache_status(self) -> dict[str, Any]:
        with self._lock:
            current = self._current
            previous = self._previous
        return {
            "algorithmBootId": self.boot_id,
            "ready": current is not None,
            "acceptedSnapshotId": current.snapshot_id if current else "",
            "previousSnapshotId": previous.snapshot_id if previous else "",
            "nodeCount": len(current.nodes) if current else 0,
            **self.metrics_cache.status(),
        }

    def calculate(self, request: dict[str, Any]) -> dict[str, Any]:
        snapshot_id = str(request.get("nodeStaticSnapshotId", ""))
        snapshot = self._get_snapshot(snapshot_id)
        max_groups = int(request.get("maxCandidateGroups", 3))
        if not 1 <= max_groups <= 3:
            raise AlgorithmError("maxCandidateGroups must be between 1 and 3")

        states = {
            str(item.get("nodeUID", "")): item
            for item in request.get("schedulerState", [])
        }
        selector = request.get("nodeRequirements", {}).get("nodeSelector", {})
        demand_spec = {"podSets": request.get("podSets", [])}
        minimums = demand_minimums(demand_spec)
        if not minimums:
            raise AlgorithmError("podSets must not be empty")

        metric_snapshot, metrics_degraded, metric_warnings = (
            self.metrics_cache.for_calculation()
        )
        metrics_by_name = metric_snapshot.nodes if metric_snapshot else {}
        nodes_by_name: dict[str, dict[str, Any]] = {}
        free_by_name: dict[str, dict[str, int]] = {}
        grouped: dict[str, list[str]] = {}
        for node in snapshot.nodes:
            node_uid = str(node["nodeUID"])
            state = states.get(node_uid)
            if not state or not state.get("ready") or state.get("unschedulable"):
                continue
            if not all(node.get("labels", {}).get(k) == v for k, v in selector.items()):
                continue
            allocatable = parse_resources(node.get("allocatable", {}))
            requested = parse_resources(state.get("requestedResources", {}))
            free = subtract_resources(allocatable, requested)
            if not any(fits(free, request_) for _, _, request_ in minimums):
                continue
            name = str(node["nodeName"])
            switch_id = str(node.get("topology", {}).get("switchId", "unknown"))
            nodes_by_name[name] = node
            free_by_name[name] = free
            grouped.setdefault(switch_id, []).append(name)

        candidates: list[dict[str, Any]] = []
        for switch_id, names in grouped.items():
            names.sort()
            if not can_place_minimums(names, free_by_name, minimums):
                continue
            node_entries = [
                self._score_node(
                    nodes_by_name[name], free_by_name[name], metrics_by_name.get(name)
                )
                for name in names
            ]
            topology_score = self._topology_score(nodes_by_name[names[0]])
            resource_score = sum(item["score"] for item in node_entries) / len(node_entries)
            group_score = round(0.7 * resource_score + 0.3 * topology_score, 2)
            candidates.append(
                {
                    "groupId": switch_id,
                    "topologyLevel": "leafGroup",
                    "groupScore": group_score,
                    "nodes": node_entries,
                }
            )

        candidates.sort(key=lambda item: (-item["groupScore"], item["groupId"]))
        candidates = candidates[:max_groups]
        for index, item in enumerate(candidates, start=1):
            item["rank"] = index

        base = {
            "requestId": str(request.get("requestId", "")),
            "taskUID": str(request.get("taskUID", "")),
            "ngdUID": str(request.get("ngdUID", "")),
            "ngdGeneration": int(request.get("ngdGeneration", 0)),
            "algorithmBootId": self.boot_id,
            "nodeStaticSnapshotId": snapshot_id,
            "schedulerStateSnapshotId": str(
                request.get("schedulerStateSnapshotId", "")
            ),
            "metricSnapshotId": (
                metric_snapshot.snapshot_id if metric_snapshot else "metrics-disabled"
            ),
            "degraded": metrics_degraded,
            "warnings": metric_warnings,
        }
        if not candidates:
            return {**base, "status": "UNSATISFIABLE", "candidateNodeGroups": []}
        return {**base, "status": "SUCCESS", "candidateNodeGroups": candidates}

    def _get_snapshot(self, snapshot_id: str) -> StaticSnapshot:
        with self._lock:
            for snapshot in (self._current, self._previous):
                if snapshot and snapshot.snapshot_id == snapshot_id:
                    return snapshot
        raise SnapshotNotReady(f"node static snapshot {snapshot_id!r} is not ready")

    @staticmethod
    def _validate_static_nodes(nodes: Any) -> list[dict[str, Any]]:
        if not isinstance(nodes, list) or not nodes:
            raise AlgorithmError("static snapshot nodes must be a non-empty array")
        seen_uids: set[str] = set()
        result: list[dict[str, Any]] = []
        for node in nodes:
            if not isinstance(node, dict):
                raise AlgorithmError("static snapshot contains a malformed node")
            name = str(node.get("nodeName", ""))
            uid = str(node.get("nodeUID", ""))
            switch_id = str(node.get("topology", {}).get("switchId", ""))
            if not name or not uid or not switch_id:
                raise AlgorithmError("every node needs nodeName, nodeUID and switchId")
            if uid in seen_uids:
                raise AlgorithmError(f"duplicate nodeUID {uid}")
            seen_uids.add(uid)
            result.append(copy.deepcopy(node))
        result.sort(key=lambda item: (str(item["nodeUID"]), str(item["nodeName"])))
        return result

    @staticmethod
    def _score_node(
        node: dict[str, Any],
        free: dict[str, int],
        metrics: dict[str, float] | None = None,
    ) -> dict[str, Any]:
        allocatable = parse_resources(node.get("allocatable", {}))
        ratios = [
            free.get(resource, 0) / capacity
            for resource in ("cpu", "memory")
            if (capacity := allocatable.get(resource, 0)) > 0
        ]
        resource_score = 100 * sum(ratios) / len(ratios) if ratios else 0
        utilizations = [
            metrics[name]
            for name in ("cpuUtilization", "memoryUtilization")
            if metrics and name in metrics
        ]
        if utilizations:
            load_score = 100 * (1 - sum(utilizations) / len(utilizations))
            score = round(0.7 * resource_score + 0.3 * load_score)
        else:
            score = round(resource_score)
        return {
            "nodeUID": str(node["nodeUID"]),
            "nodeName": str(node["nodeName"]),
            "score": max(0, min(100, score)),
        }

    @staticmethod
    def _topology_score(node: dict[str, Any]) -> float:
        topology = node.get("topology", {})
        bandwidth = max(0.0, float(topology.get("bandwidthGbps", 10)))
        latency = max(0.001, float(topology.get("latencyMillis", 5)))
        bandwidth_score = min(100.0, bandwidth / 25.0 * 100.0)
        latency_score = min(100.0, 1.0 / latency * 100.0)
        return 0.7 * bandwidth_score + 0.3 * latency_score
