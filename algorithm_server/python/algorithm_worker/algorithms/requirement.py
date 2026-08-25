"""固定流水线 FILTER 阶段：根据 NGD/任务硬约束生成可用 Node 集合。"""

from __future__ import annotations

from typing import Any

from ..context import AllocationContext
from ..errors import InvalidAlgorithmParameters
from ..models import AlgorithmStage
from ..services.node_view_builder import NodeViewBuilder


class RequirementAlgorithm:
    """固定执行的需求过滤算法，不接受 NGD 自定义编排参数。"""

    name = "requirement"
    version = "v1"
    stage = AlgorithmStage.FILTER

    def __init__(self, node_view_builder: NodeViewBuilder | None = None) -> None:
        self.node_view_builder = node_view_builder or NodeViewBuilder()

    def validate_parameters(self, parameters: dict[str, Any]) -> None:
        """固定算法当前没有外部参数。"""

        if parameters:
            raise InvalidAlgorithmParameters(
                "fixed requirement algorithm does not accept parameters"
            )

    def execute(
        self,
        context: AllocationContext,
        parameters: dict[str, Any],
    ) -> dict[str, Any]:
        """返回字段会由 PipelineRunner 写入 AllocationContext。"""

        return {
            "current_nodes": self.node_view_builder.build(context),
        }
