from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from .cache.metrics import MetricSnapshot
from .cache.static_nodes import StaticNodeSnapshot


@dataclass
class AllocationContext:
    request: dict[str, Any]
    static_snapshot: StaticNodeSnapshot
    metric_snapshot: MetricSnapshot | None
    metrics_degraded: bool
    warnings: list[str]
    pod_minimums: list[tuple[str, int, dict[str, int]]]
    current_nodes: list[dict[str, Any]] = field(default_factory=list)
    node_groups: list[dict[str, Any]] = field(default_factory=list)
    candidates: list[dict[str, Any]] = field(default_factory=list)
    required_distinct_nodes: int = 0
