from __future__ import annotations

from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse

from .cache.metrics import MetricsCache
from .errors import AlgorithmError
from .pipeline import AlgorithmService


metrics_cache = MetricsCache.from_env()
service = AlgorithmService(metrics_cache=metrics_cache)


@asynccontextmanager
async def lifespan(_: FastAPI):
    metrics_cache.start()
    try:
        yield
    finally:
        metrics_cache.stop()


app = FastAPI(
    title="NGD/NGG Algorithm API Server",
    version="1.2.0",
    lifespan=lifespan,
)


@app.exception_handler(AlgorithmError)
async def algorithm_error_handler(
    _: Request,
    exc: AlgorithmError,
) -> JSONResponse:
    return JSONResponse(
        status_code=exc.status_code,
        content=exc.response(),
    )


@app.get("/healthz")
def healthz() -> dict[str, Any]:
    return service.health()


@app.get("/readyz")
def readyz() -> dict[str, Any]:
    return service.ready()


@app.get("/internal/v1/cache/status")
def cache_status() -> dict[str, Any]:
    return service.cache_status()


@app.get("/internal/v1/node-static-cache/status")
def legacy_static_cache_status() -> dict[str, Any]:
    status = service.cache_status()
    node_static = status["nodeStatic"]
    return {
        "algorithmBootId": status["bootId"],
        "ready": node_static["ready"],
        "acceptedSnapshotId": node_static["currentSnapshotId"],
        "previousSnapshotId": node_static["previousSnapshotId"],
        "nodeCount": node_static["nodeCount"],
        "metricSnapshotId": status["metrics"]["currentSnapshotId"],
    }


@app.put("/internal/v1/node-static-snapshots/{snapshot_id}")
def put_static_snapshot(
    snapshot_id: str,
    body: dict[str, Any],
) -> dict[str, Any]:
    return service.put_static_snapshot(snapshot_id, body)


@app.post("/api/v1/allocate")
def allocate(body: dict[str, Any]) -> dict[str, Any]:
    return service.allocate(body)


@app.post("/api/v1/node-groups/calculate")
def legacy_calculate(body: dict[str, Any]) -> dict[str, Any]:
    return service.calculate(body)
