"""定义算法阶段枚举和所有可插拔算法必须满足的接口。"""

from __future__ import annotations

from enum import IntEnum
from typing import Any, Protocol, TYPE_CHECKING

if TYPE_CHECKING:
    from .context import AllocationContext


class AlgorithmStage(IntEnum):
    """数值顺序同时用于验证流水线只能由过滤走向评分。"""

    FILTER = 10
    GROUP = 20
    SCORE = 30


class AlgorithmPlugin(Protocol):
    """算法插件协议；新增插件需提供身份、阶段、校验和执行方法。"""

    name: str
    version: str
    stage: AlgorithmStage

    def validate_parameters(self, parameters: dict[str, Any]) -> None:
        ...

    def execute(
        self,
        context: "AllocationContext",
        parameters: dict[str, Any],
    ) -> dict[str, Any]:
        ...
