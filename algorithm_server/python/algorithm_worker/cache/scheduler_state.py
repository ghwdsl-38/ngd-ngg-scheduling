from __future__ import annotations

from typing import Any, Iterable

from ..errors import InvalidRequest
from ..quantity import parse_resources


class RequestSchedulerState:
    """Normalizes request-scoped Node usage state.

    This module intentionally does not keep cross-request state. It lives under
    cache/ only to make the dynamic-state boundary explicit and to preserve the
    requested project layout.
    """

    def normalize(
        self,
        request: dict[str, Any],
        static_nodes: Iterable[dict[str, Any]],
    ) -> list[dict[str, Any]]:
        nodes = tuple(static_nodes)
        raw_states = request.get("nodeUsageStates")
        if raw_states is None:
            return self._from_legacy_scheduler_state(request, nodes)
        if not isinstance(raw_states, list):
            raise InvalidRequest("nodeUsageStates must be an array")
        known = {str(node["nodeUID"]) for node in nodes}
        seen: set[str] = set()
        result: list[dict[str, Any]] = []
        for item in raw_states:
            if not isinstance(item, dict):
                raise InvalidRequest("nodeUsageStates contains a malformed entry")
            uid = str(item.get("nodeUID", ""))
            if not uid or uid not in known:
                raise InvalidRequest(
                    f"nodeUsageStates contains unknown Node UID {uid!r}"
                )
            if uid in seen:
                raise InvalidRequest(f"duplicate nodeUsageStates entry for {uid}")
            if not isinstance(item.get("inUse"), bool):
                raise InvalidRequest(
                    f"nodeUsageStates[{uid}].inUse must be boolean"
                )
            result.append({"nodeUID": uid, "inUse": item["inUse"]})
            seen.add(uid)
        return result

    @staticmethod
    def _from_legacy_scheduler_state(
        request: dict[str, Any],
        static_nodes: tuple[dict[str, Any], ...],
    ) -> list[dict[str, Any]]:
        states = request.get("schedulerState", [])
        by_uid = {
            str(item.get("nodeUID", "")): item
            for item in states
            if isinstance(item, dict)
        }
        result: list[dict[str, Any]] = []
        for node in static_nodes:
            uid = str(node["nodeUID"])
            state = by_uid.get(uid)
            if state is None:
                result.append({"nodeUID": uid, "inUse": True})
                continue
            unavailable = not bool(state.get("ready", False)) or bool(
                state.get("unschedulable", False)
            )
            if not unavailable:
                allocatable = parse_resources(node.get("allocatable", {}))
                requested = parse_resources(state.get("requestedResources", {}))
                unavailable = bool(allocatable) and all(
                    requested.get(name, 0) >= value
                    for name, value in allocatable.items()
                    if value > 0
                )
            result.append({"nodeUID": uid, "inUse": unavailable})
        return result
