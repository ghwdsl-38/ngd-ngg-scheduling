from __future__ import annotations

from typing import Any

from ..context import AllocationContext
from ..errors import InvalidAlgorithmParameters
from ..models import AlgorithmStage
from ..services.node_view_builder import NodeViewBuilder


class RequirementAlgorithm:
    name = "requirement"
    version = "v1"
    stage = AlgorithmStage.FILTER

    def __init__(self, node_view_builder: NodeViewBuilder | None = None) -> None:
        self.node_view_builder = node_view_builder or NodeViewBuilder()

    def validate_parameters(self, parameters: dict[str, Any]) -> None:
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
        return {
            "current_nodes": self.node_view_builder.build(context),
            "required_distinct_nodes": int(
                parameters.get("requiredDistinctNodes", 0)
            ),
        }
