from __future__ import annotations

from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI, HTTPException

from .engine import AlgorithmEngine, AlgorithmError, SnapshotNotReady
from .metrics import MetricsCache


metrics_cache = MetricsCache.from_env()
engine = AlgorithmEngine(metrics_cache=metrics_cache)


@asynccontextmanager
async def lifespan(_: FastAPI):
    metrics_cache.start()
    try:
        yield
    finally:
        metrics_cache.stop()


app = FastAPI(
    title="NGD/NGG Algorithm API Server", version="0.3.0", lifespan=lifespan
)


@app.get("/healthz")
def healthz() -> dict[str, Any]:
    return {"status": "ok", **engine.cache_status()}


@app.get("/internal/v1/node-static-cache/status")
def static_cache_status() -> dict[str, Any]:
    return engine.cache_status()


@app.put("/internal/v1/node-static-snapshots/{snapshot_id}")
def put_static_snapshot(snapshot_id: str, body: dict[str, Any]) -> dict[str, Any]:
    try:
        return engine.put_static_snapshot(snapshot_id, body)
    except AlgorithmError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.post("/api/v1/node-groups/calculate")
def calculate(body: dict[str, Any]) -> dict[str, Any]:
    try:
        return engine.calculate(body)
    except SnapshotNotReady as exc:
        raise HTTPException(status_code=409, detail=str(exc)) from exc
    except AlgorithmError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
