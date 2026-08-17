from __future__ import annotations

from dataclasses import dataclass

from .metrics import MetricSnapshot, MetricsCache
from .static_nodes import StaticNodeCache, StaticNodeSnapshot


@dataclass(frozen=True)
class ResolvedSnapshots:
    static: StaticNodeSnapshot
    metrics: MetricSnapshot | None
    metrics_degraded: bool
    metric_warnings: list[str]


class SnapshotResolver:
    def __init__(
        self,
        static_nodes: StaticNodeCache,
        metrics: MetricsCache,
    ) -> None:
        self.static_nodes = static_nodes
        self.metrics = metrics

    def resolve(
        self,
        static_snapshot_id: str,
        request_id: str,
    ) -> ResolvedSnapshots:
        static = self.static_nodes.get(static_snapshot_id, request_id)
        metrics, degraded, warnings = self.metrics.for_calculation()
        return ResolvedSnapshots(
            static=static,
            metrics=metrics,
            metrics_degraded=degraded,
            metric_warnings=warnings,
        )
