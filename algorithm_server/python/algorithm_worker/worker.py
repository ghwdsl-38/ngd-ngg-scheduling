"""Single Python algorithm worker managed by the Go Algorithm API process.

The worker owns algorithm implementations only. Static Node snapshots,
Prometheus snapshots, HTTP and cross-request cache state are owned by Go and
are sent here as a fully resolved calculation context for every request.
"""

from __future__ import annotations

import json
import sys
import traceback
from typing import Any

from .cache.metrics import MetricSnapshot
from .cache.static_nodes import StaticNodeSnapshot
from .context import AllocationContext
from .errors import AlgorithmError, InvalidRequest
from .pipeline import PipelineRunner
from .quantity import pod_set_minimums


class AlgorithmWorker:
    def __init__(self) -> None:
        self.pipeline = PipelineRunner()

    def calculate(self, payload: dict[str, Any]) -> dict[str, Any]:
        request = payload.get("request")
        static = payload.get("staticSnapshot")
        metric = payload.get("metricSnapshot")
        if not isinstance(request, dict) or not isinstance(static, dict):
            raise InvalidRequest("worker requires request and staticSnapshot")

        nodes = static.get("nodes")
        if not isinstance(nodes, list):
            raise InvalidRequest("staticSnapshot.nodes must be an array")
        static_snapshot = StaticNodeSnapshot(
            snapshot_id=str(static.get("snapshotId", "")),
            cluster_id=str(static.get("clusterId", "")),
            topology_version=str(static.get("topologyVersion", "")),
            nodes=tuple(nodes),
        )

        metric_snapshot = None
        if isinstance(metric, dict):
            metric_nodes = metric.get("nodes", {})
            if not isinstance(metric_nodes, dict):
                raise InvalidRequest("metricSnapshot.nodes must be an object")
            metric_snapshot = MetricSnapshot(
                snapshot_id=str(metric.get("snapshotId", "")),
                captured_at=float(metric.get("capturedAtUnix", 0)),
                nodes=metric_nodes,
            )

        context = AllocationContext(
            request=request,
            static_snapshot=static_snapshot,
            metric_snapshot=metric_snapshot,
            metrics_degraded=bool(payload.get("metricsDegraded", True)),
            warnings=[str(item) for item in payload.get("warnings", [])],
            pod_minimums=pod_set_minimums(request.get("podSets", [])),
        )
        candidates = self.pipeline.run(context)
        return {"candidateNodeGroups": candidates}


def _error(exc: Exception, request_id: str) -> dict[str, Any]:
    if isinstance(exc, AlgorithmError):
        return {
            "code": exc.code,
            "message": str(exc),
            "retryable": exc.retryable,
            "statusCode": exc.status_code,
            "requestId": exc.request_id or request_id,
        }
    traceback.print_exc(file=sys.stderr)
    return {
        "code": "ALGORITHM_WORKER_ERROR",
        "message": str(exc),
        "retryable": True,
        "statusCode": 500,
        "requestId": request_id,
    }


def main() -> int:
    worker = AlgorithmWorker()
    for line in sys.stdin:
        if not line.strip():
            continue
        message_id = ""
        request_id = ""
        try:
            message = json.loads(line)
            message_id = str(message.get("id", ""))
            payload = message.get("payload", {})
            if isinstance(payload, dict):
                request = payload.get("request", {})
                if isinstance(request, dict):
                    request_id = str(request.get("requestId", ""))
            response = {
                "id": message_id,
                "result": worker.calculate(payload),
            }
        except Exception as exc:  # process stays alive after a bad request
            response = {
                "id": message_id,
                "error": _error(exc, request_id),
            }
        sys.stdout.write(
            json.dumps(response, ensure_ascii=False, separators=(",", ":"))
            + "\n"
        )
        sys.stdout.flush()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
