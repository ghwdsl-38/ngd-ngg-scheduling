from __future__ import annotations

import copy
import hashlib
import json
import threading
from dataclasses import dataclass
from typing import Any

from ..errors import InvalidRequest, StaticSnapshotNotFound


def canonical_hash(value: Any) -> str:
    encoded = json.dumps(
        value,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return "sha256:" + hashlib.sha256(encoded).hexdigest()


@dataclass(frozen=True)
class StaticNodeSnapshot:
    snapshot_id: str
    cluster_id: str
    topology_version: str
    nodes: tuple[dict[str, Any], ...]


class StaticNodeCache:
    """Thread-safe current/previous Node static snapshot cache."""

    def __init__(self) -> None:
        self._lock = threading.RLock()
        self._current: StaticNodeSnapshot | None = None
        self._previous: StaticNodeSnapshot | None = None

    def put(self, snapshot_id: str, body: dict[str, Any]) -> StaticNodeSnapshot:
        if not snapshot_id.startswith("sha256:"):
            raise InvalidRequest("snapshotId must use sha256:<hex> format")
        content = {
            "clusterId": str(body.get("clusterId", "")),
            "topologyVersion": str(body.get("topologyVersion", "")),
            "nodes": body.get("nodes", []),
        }
        actual = canonical_hash(content)
        if actual != snapshot_id:
            raise InvalidRequest(
                f"snapshot checksum mismatch: path={snapshot_id} actual={actual}"
            )
        nodes = self._validate_nodes(content["nodes"])
        snapshot = StaticNodeSnapshot(
            snapshot_id=snapshot_id,
            cluster_id=content["clusterId"],
            topology_version=content["topologyVersion"],
            nodes=tuple(nodes),
        )
        with self._lock:
            if self._current and self._current.snapshot_id == snapshot_id:
                return self._current
            if self._current:
                self._previous = self._current
            self._current = snapshot
        return snapshot

    def get(
        self,
        snapshot_id: str,
        request_id: str = "",
    ) -> StaticNodeSnapshot:
        with self._lock:
            for snapshot in (self._current, self._previous):
                if snapshot and snapshot.snapshot_id == snapshot_id:
                    return snapshot
        raise StaticSnapshotNotFound(
            f"node static snapshot {snapshot_id!r} is not available",
            request_id=request_id,
        )

    def status(self) -> dict[str, Any]:
        with self._lock:
            current = self._current
            previous = self._previous
        return {
            "ready": current is not None,
            "currentSnapshotId": current.snapshot_id if current else "",
            "previousSnapshotId": previous.snapshot_id if previous else "",
            "nodeCount": len(current.nodes) if current else 0,
        }

    @staticmethod
    def _validate_nodes(nodes: Any) -> list[dict[str, Any]]:
        if not isinstance(nodes, list) or not nodes:
            raise InvalidRequest("static snapshot nodes must be a non-empty array")
        seen_uids: set[str] = set()
        normalized: list[dict[str, Any]] = []
        for raw in nodes:
            if not isinstance(raw, dict):
                raise InvalidRequest("static snapshot contains a malformed Node")
            node = copy.deepcopy(raw)
            name = str(node.get("nodeName", ""))
            uid = str(node.get("nodeUID", ""))
            topology = node.get("topology", {})
            if not isinstance(topology, dict):
                raise InvalidRequest(f"Node {name or uid!r} has malformed topology")
            leaf = str(topology.get("leafSwitchId") or topology.get("switchId") or "")
            core = str(topology.get("coreSwitchId", ""))
            if not name or not uid or not leaf or not core:
                raise InvalidRequest(
                    "every Node needs nodeName, nodeUID, coreSwitchId and leafSwitchId"
                )
            if uid in seen_uids:
                raise InvalidRequest(f"duplicate nodeUID {uid}")
            seen_uids.add(uid)
            topology["leafSwitchId"] = leaf
            topology["coreSwitchId"] = core
            node["topology"] = topology
            normalized.append(node)
        normalized.sort(
            key=lambda item: (str(item["nodeUID"]), str(item["nodeName"]))
        )
        return normalized
