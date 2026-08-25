"""定义一次算法计算使用的不可变快照和可变流水线上下文。"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

@dataclass(frozen=True)
class StaticNodeSnapshot:
    """PRC 上传并由 Go 按内容 Hash 解析出的 Node 静态快照。"""

    snapshot_id: str
    cluster_id: str
    topology_version: str
    nodes: tuple[dict[str, Any], ...]


@dataclass(frozen=True)
class MetricSnapshot:
    """Go 从 Prometheus 获取的某一时刻 Node CPU、内存和网络指标快照。"""

    snapshot_id: str
    captured_at: float
    nodes: dict[str, dict[str, float]]


@dataclass
class AllocationContext:
    """仅在一次请求中流转的数据；各插件把阶段结果写入对应字段。"""

    request: dict[str, Any]
    static_snapshot: StaticNodeSnapshot
    metric_snapshot: MetricSnapshot | None
    metrics_degraded: bool
    warnings: list[str]
    pod_minimums: list[tuple[str, int, dict[str, int]]]
    current_nodes: list[dict[str, Any]] = field(default_factory=list)
    node_groups: list[dict[str, Any]] = field(default_factory=list)
    candidates: list[dict[str, Any]] = field(default_factory=list)
    # 只在请求显式 debugTrace=true 时填充，不参与算法决策。
    pipeline_trace: list[dict[str, Any]] = field(default_factory=list)
