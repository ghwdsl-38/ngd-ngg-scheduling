"""定义可跨 Go/Python 边界返回的结构化算法业务错误。"""

from __future__ import annotations


class AlgorithmError(ValueError):
    """业务错误基类，携带 HTTP 语义和是否允许 PRC 重试的信息。"""

    code = "ALGORITHM_ERROR"
    status_code = 400
    retryable = False

    def __init__(
        self,
        message: str,
        *,
        request_id: str = "",
        code: str | None = None,
        status_code: int | None = None,
        retryable: bool | None = None,
    ) -> None:
        super().__init__(message)
        self.request_id = request_id
        if code is not None:
            self.code = code
        if status_code is not None:
            self.status_code = status_code
        if retryable is not None:
            self.retryable = retryable

    def response(self) -> dict[str, object]:
        return {
            "requestId": self.request_id,
            "code": self.code,
            "message": str(self),
            "retryable": self.retryable,
        }


class InvalidRequest(AlgorithmError):
    """请求结构、资源数量或快照内容不合法。"""
    code = "INVALID_REQUEST"
    status_code = 400


class InvalidAlgorithmParameters(AlgorithmError):
    """服务端固定算法参数名称或取值不受当前版本支持。"""
    code = "INVALID_ALGORITHM_PARAMETERS"
    status_code = 422


class RequiredMetricsNotReady(AlgorithmError):
    """任务强制要求指标，但 Prometheus 快照未就绪或已降级。"""
    code = "REQUIRED_METRICS_NOT_READY"
    status_code = 503
    retryable = True
