"""定义固定算法流水线使用的阶段枚举。"""

from __future__ import annotations

from enum import IntEnum


class AlgorithmStage(IntEnum):
    """数值顺序同时用于验证流水线只能由过滤走向评分。"""

    FILTER = 10
    GROUP = 20
    SCORE = 30
