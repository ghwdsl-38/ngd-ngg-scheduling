"""由 Go Algorithm API 主进程管理的单一 Python 算法 Worker。

Worker 只拥有算法实现。Node 静态快照、Prometheus 快照、HTTP 和跨请求缓存
由 Go 持有，并在每次请求中以完整计算上下文发送给本进程。
"""

from __future__ import annotations

import json
import sys
import traceback
from typing import Any

from .context import AllocationContext, MetricSnapshot, StaticNodeSnapshot
from .errors import AlgorithmError, InvalidRequest
from .pipeline import PipelineRunner


class AlgorithmWorker:
    """把 Go 传入的普通 JSON 对象转换为上下文并执行算法流水线。"""

    def __init__(self) -> None:
        self.pipeline = PipelineRunner()

    def calculate(self, payload: dict[str, Any]) -> dict[str, Any]:
        """执行一次无跨请求副作用的候选组计算。"""

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

        # 正式资源池 NGD 没有 PodSet。maxNodes 是候选节点数量上限，不是
        # 必须节点数；minResources/quota 由固定流水线直接读取 NGD 处理。
        if request.get("requestMode") != "resourcePool" or not isinstance(
            request.get("ngd"), dict
        ):
            raise InvalidRequest("worker requires a resourcePool request with original ngd spec")

        context = AllocationContext(
            request=request,
            static_snapshot=static_snapshot,
            metric_snapshot=metric_snapshot,
            metrics_degraded=bool(payload.get("metricsDegraded", True)),
            # Go 的 nil slice 会编码为 JSON null；把 null 和缺失都统一成空列表。
            warnings=[str(item) for item in (payload.get("warnings") or [])],
            pod_minimums=[],
        )
        candidates = self.pipeline.run(context)
        result = {"candidateNodeGroups": candidates}
        if request.get("debugTrace") is True:
            result["pipelineTrace"] = context.pipeline_trace
        return result


def _error(exc: Exception, request_id: str) -> dict[str, Any]:
    """把已知业务异常或未知异常转换为 Go 可识别的错误对象。"""

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
    """持续读取一行请求并写出一行响应；单次坏请求不会退出进程。"""

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
