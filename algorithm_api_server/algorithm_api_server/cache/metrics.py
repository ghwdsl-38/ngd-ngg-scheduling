from __future__ import annotations

import copy
import os
import threading
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Any, Callable

from ..collectors.prometheus_collector import PrometheusCollector
from .static_nodes import canonical_hash


@dataclass(frozen=True)
class MetricSnapshot:
    snapshot_id: str
    captured_at: float
    nodes: dict[str, dict[str, float]]


class MetricsCache:
    """Process-local current/previous Prometheus metric snapshots."""

    def __init__(
        self,
        prometheus_url: str = "",
        interval_seconds: float = 30,
        stale_after_seconds: float = 120,
        node_label: str = "node",
        cpu_query: str = "",
        memory_query: str = "",
        request_timeout_seconds: float = 5,
        query: Callable[[str], list[dict[str, Any]]] | None = None,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self.prometheus_url = prometheus_url.rstrip("/")
        self.interval_seconds = max(1.0, interval_seconds)
        self.stale_after_seconds = max(self.interval_seconds, stale_after_seconds)
        self.node_label = node_label
        self.cpu_query = cpu_query
        self.memory_query = memory_query
        collector = PrometheusCollector(
            self.prometheus_url,
            request_timeout_seconds,
        )
        self._query = query or collector.query
        self._clock = clock
        self._lock = threading.RLock()
        self._current: MetricSnapshot | None = None
        self._previous: MetricSnapshot | None = None
        self._last_error = ""
        self._last_query_at = 0.0
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None

    @classmethod
    def from_env(cls) -> "MetricsCache":
        return cls(
            prometheus_url=os.getenv("PROMETHEUS_URL", ""),
            interval_seconds=float(os.getenv("PROMETHEUS_REFRESH_SECONDS", "30")),
            stale_after_seconds=float(os.getenv("PROMETHEUS_STALE_SECONDS", "120")),
            request_timeout_seconds=float(
                os.getenv("PROMETHEUS_REQUEST_TIMEOUT_SECONDS", "5")
            ),
            node_label=os.getenv("PROMETHEUS_NODE_LABEL", "node"),
            cpu_query=os.getenv(
                "PROMETHEUS_CPU_QUERY",
                '1 - avg by (node) (rate(node_cpu_seconds_total{mode="idle"}[5m]))',
            ),
            memory_query=os.getenv(
                "PROMETHEUS_MEMORY_QUERY",
                "1 - (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)",
            ),
        )

    @property
    def enabled(self) -> bool:
        return bool(self.prometheus_url and (self.cpu_query or self.memory_query))

    def start(self) -> None:
        if not self.enabled or (self._thread and self._thread.is_alive()):
            return
        self._stop.clear()
        self._thread = threading.Thread(
            target=self._run,
            name="prometheus-metrics-cache",
            daemon=True,
        )
        self._thread.start()

    def stop(self) -> None:
        self._stop.set()
        if self._thread is not None:
            self._thread.join(timeout=min(5.0, self.interval_seconds + 1))
        self._thread = None

    def refresh(self) -> MetricSnapshot:
        if not self.enabled:
            raise RuntimeError("Prometheus metrics cache is disabled")
        values: dict[str, dict[str, float]] = {}
        if self.cpu_query:
            self._merge(values, "cpuUsageRatio", self._query(self.cpu_query))
        if self.memory_query:
            self._merge(values, "memoryUsageRatio", self._query(self.memory_query))
        if not values:
            raise RuntimeError("Prometheus returned no Node metrics")
        captured_at = self._clock()
        canonical_nodes = {name: values[name] for name in sorted(values)}
        snapshot = MetricSnapshot(
            snapshot_id=canonical_hash({"nodes": canonical_nodes}),
            captured_at=captured_at,
            nodes=copy.deepcopy(canonical_nodes),
        )
        with self._lock:
            if self._current and self._current.snapshot_id != snapshot.snapshot_id:
                self._previous = self._current
            self._current = snapshot
            self._last_query_at = captured_at
            self._last_error = ""
        return snapshot

    def for_calculation(self) -> tuple[MetricSnapshot | None, bool, list[str]]:
        with self._lock:
            snapshot = self._current
            error = self._last_error
        if not self.enabled:
            return None, True, ["Prometheus metrics cache is disabled"]
        if snapshot is None:
            return None, True, [error or "Prometheus metric snapshot is not ready"]
        age = max(0.0, self._clock() - snapshot.captured_at)
        if age > self.stale_after_seconds:
            warning = f"Prometheus metric snapshot is stale: ageSeconds={round(age, 1)}"
            if error:
                warning += f"; lastError={error}"
            return snapshot, True, [warning]
        return snapshot, False, []

    def status(self) -> dict[str, Any]:
        snapshot, degraded, warnings = self.for_calculation()
        with self._lock:
            previous = self._previous
            last_error = self._last_error
            last_query_at = self._last_query_at
        return {
            "ready": snapshot is not None,
            "enabled": self.enabled,
            "degraded": degraded,
            "currentSnapshotId": (
                snapshot.snapshot_id if snapshot else "metrics-disabled"
            ),
            "previousSnapshotId": previous.snapshot_id if previous else "",
            "capturedAt": (
                self._timestamp(snapshot.captured_at) if snapshot else ""
            ),
            "lastQueryAt": self._timestamp(last_query_at) if last_query_at else "",
            "nodeCount": len(snapshot.nodes) if snapshot else 0,
            "lastError": last_error or None,
            "warnings": warnings,
        }

    def _run(self) -> None:
        while not self._stop.is_set():
            try:
                self.refresh()
            except Exception as exc:
                with self._lock:
                    self._last_query_at = self._clock()
                    self._last_error = str(exc)
            self._stop.wait(self.interval_seconds)

    def _merge(
        self,
        destination: dict[str, dict[str, float]],
        field: str,
        results: list[dict[str, Any]],
    ) -> None:
        for item in results:
            labels = item.get("metric", {})
            node_name = str(labels.get(self.node_label, ""))
            raw_value = item.get("value", [None, None])
            if not node_name or not isinstance(raw_value, list) or len(raw_value) != 2:
                continue
            try:
                value = max(0.0, min(1.0, float(raw_value[1])))
            except (TypeError, ValueError):
                continue
            destination.setdefault(node_name, {})[field] = value

    @staticmethod
    def _timestamp(value: float) -> str:
        return (
            datetime.fromtimestamp(value, tz=timezone.utc)
            .isoformat()
            .replace("+00:00", "Z")
        )
