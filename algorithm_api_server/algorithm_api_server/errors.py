from __future__ import annotations


class AlgorithmError(ValueError):
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
    code = "INVALID_REQUEST"
    status_code = 400


class StaticSnapshotNotFound(AlgorithmError):
    code = "STATIC_SNAPSHOT_NOT_FOUND"
    status_code = 409
    retryable = True


class UnknownAlgorithm(AlgorithmError):
    code = "UNKNOWN_ALGORITHM"
    status_code = 422


class InvalidAlgorithmOrder(AlgorithmError):
    code = "INVALID_ALGORITHM_ORDER"
    status_code = 422


class InvalidAlgorithmParameters(AlgorithmError):
    code = "INVALID_ALGORITHM_PARAMETERS"
    status_code = 422


class RequiredMetricsNotReady(AlgorithmError):
    code = "REQUIRED_METRICS_NOT_READY"
    status_code = 503
    retryable = True
