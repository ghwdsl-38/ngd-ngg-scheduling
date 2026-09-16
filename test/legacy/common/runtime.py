#!/usr/bin/env python3
"""三组演示共用的运行编号、JSON落盘和统计函数。"""

from __future__ import annotations

import json
import math
from datetime import datetime
from pathlib import Path
from typing import Any


def run_id() -> str:
    return datetime.now().strftime("%Y%m%d-%H%M%S")


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def percentile(values: list[float], percentage: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    position = (len(ordered) - 1) * percentage
    lower = math.floor(position)
    upper = math.ceil(position)
    if lower == upper:
        return ordered[lower]
    return ordered[lower] + (ordered[upper] - ordered[lower]) * (position - lower)


def distribution(values: list[float]) -> dict[str, float | int]:
    if not values:
        return {"count": 0, "minMs": 0, "meanMs": 0, "p50Ms": 0, "p95Ms": 0, "p99Ms": 0, "maxMs": 0}
    return {
        "count": len(values),
        "minMs": round(min(values), 3),
        "meanMs": round(sum(values) / len(values), 3),
        "p50Ms": round(percentile(values, 0.50), 3),
        "p95Ms": round(percentile(values, 0.95), 3),
        "p99Ms": round(percentile(values, 0.99), 3),
        "maxMs": round(max(values), 3),
    }
