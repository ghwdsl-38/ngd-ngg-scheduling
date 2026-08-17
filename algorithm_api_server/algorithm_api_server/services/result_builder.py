from __future__ import annotations

import copy
from typing import Any

from ..context import AllocationContext


class ResultBuilder:
    def build(
        self,
        context: AllocationContext,
        *,
        boot_id: str,
        legacy_contract: bool,
    ) -> dict[str, Any]:
        request = context.request
        candidates = copy.deepcopy(context.candidates)
        if legacy_contract:
            self._convert_legacy_topology(candidates)

        metric_id = (
            context.metric_snapshot.snapshot_id
            if context.metric_snapshot
            else "metrics-disabled"
        )
        response: dict[str, Any] = {
            "requestId": str(request["requestId"]),
            "taskUID": str(request["taskUID"]),
            "ngdUID": str(request["ngdUID"]),
            "ngdGeneration": int(request["ngdGeneration"]),
            "algorithmBootId": boot_id,
            "nodeStaticSnapshotId": context.static_snapshot.snapshot_id,
            "metricsSnapshotId": metric_id,
            "metricSnapshotId": metric_id,
            "degraded": context.metrics_degraded,
            "warnings": list(context.warnings),
            "status": "SUCCESS" if candidates else "UNSATISFIABLE",
            "candidateNodeGroups": candidates,
        }
        if "schedulerStateSnapshotId" in request:
            response["schedulerStateSnapshotId"] = str(
                request["schedulerStateSnapshotId"]
            )
        if not candidates:
            response["reason"] = "NO_FEASIBLE_NODE_GROUP"
        return response

    @staticmethod
    def _convert_legacy_topology(
        candidates: list[dict[str, Any]],
    ) -> None:
        for group in candidates:
            if group["topologyLevel"] == "leafSwitch":
                group["topologyLevel"] = "leafGroup"
            if group["groupId"].startswith("leaf:"):
                group["groupId"] = group["groupId"][5:]
            elif group["groupId"].startswith("core:"):
                group["groupId"] = group["groupId"][5:]
