from __future__ import annotations

from enum import IntEnum
from typing import Any, Protocol, TYPE_CHECKING

if TYPE_CHECKING:
    from .context import AllocationContext


class AlgorithmStage(IntEnum):
    FILTER = 10
    GROUP = 20
    SCORE = 30


class AlgorithmPlugin(Protocol):
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
