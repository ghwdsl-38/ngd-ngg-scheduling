"""FILTER 阶段：根据任务硬约束生成当前可用 Node 集合。"""

from __future__ import annotations

from typing import Any

from ..context import AllocationContext
from ..errors import InvalidAlgorithmParameters
from ..models import AlgorithmStage
from ..services.node_view_builder import NodeViewBuilder


class RequirementAlgorithm:
    """需求过滤插件，同时传递任务要求的最少不同节点数。"""

    name = "requirement"
    version = "v1"
    stage = AlgorithmStage.FILTER

    def __init__(self, node_view_builder: NodeViewBuilder | None = None) -> None:
        self.node_view_builder = node_view_builder or NodeViewBuilder()

    def validate_parameters(self, parameters: dict[str, Any]) -> None:
        """只接受非负的 requiredDistinctNodes 参数。"""

        allowed = {"requiredDistinctNodes"}
        unknown = sorted(set(parameters) - allowed)
        if unknown:
            raise InvalidAlgorithmParameters(
                f"requirement/v1 unknown parameters: {', '.join(unknown)}"
            )
        value = int(parameters.get("requiredDistinctNodes", 0))
        if value < 0:
            raise InvalidAlgorithmParameters(
                "requiredDistinctNodes must be zero or greater"
            )

    def execute(
        self,
        context: AllocationContext,
        parameters: dict[str, Any],
    ) -> dict[str, Any]:
        """返回字段会由 PipelineRunner 写入 AllocationContext。"""

        return {
            "current_nodes": self.node_view_builder.build(context),
            "required_distinct_nodes": int(
                parameters.get("requiredDistinctNodes", 0)
            ),
        }
