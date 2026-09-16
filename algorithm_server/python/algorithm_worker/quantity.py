"""Operate on resource values normalized by the Go Algorithm boundary."""

from __future__ import annotations

from typing import Any

from .errors import InvalidRequest


def parse_normalized_resources(resources: dict[str, Any]) -> dict[str, int]:
    """Parse decimal base-unit strings; CPU is millicores and memory is bytes."""

    if not isinstance(resources, dict):
        raise InvalidRequest("normalized resources must be an object")
    result: dict[str, int] = {}
    for name, raw in resources.items():
        try:
            value = int(str(raw))
        except (TypeError, ValueError) as exc:
            raise InvalidRequest(
                f"normalized resource {name} must be an integer"
            ) from exc
        if value < 0:
            raise InvalidRequest(
                f"normalized resource {name} must not be negative"
            )
        result[name] = value
    return result


def subtract(
    available: dict[str, int],
    request: dict[str, int],
) -> dict[str, int]:
    result = dict(available)
    for name, value in request.items():
        result[name] = max(0, result.get(name, 0) - value)
    return result
