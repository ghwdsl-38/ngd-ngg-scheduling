"""解析 Kubernetes Quantity并计算节点剩余资源。"""

from __future__ import annotations

import re
from decimal import Decimal
from typing import Any

from .errors import InvalidRequest


_QUANTITY = re.compile(r"^([0-9]+(?:\.[0-9]+)?)([A-Za-z]+)?$")
_BINARY = {
    "Ki": Decimal(1024),
    "Mi": Decimal(1024) ** 2,
    "Gi": Decimal(1024) ** 3,
    "Ti": Decimal(1024) ** 4,
}


def parse_resources(resources: dict[str, Any]) -> dict[str, int]:
    """把 CPU 转为毫核、内存转为字节、扩展资源转为整数。"""

    result: dict[str, int] = {}
    for name, raw in resources.items():
        value = str(raw)
        if name == "cpu":
            result[name] = (
                int(Decimal(value[:-1]))
                if value.endswith("m")
                else int(Decimal(value) * 1000)
            )
            continue
        match = _QUANTITY.match(value)
        if not match:
            raise InvalidRequest(f"unsupported Kubernetes quantity: {value}")
        number, suffix = match.groups()
        if not suffix:
            result[name] = int(Decimal(number))
        elif suffix in _BINARY:
            result[name] = int(Decimal(number) * _BINARY[suffix])
        elif "/" in name:
            result[name] = int(Decimal(number))
        else:
            raise InvalidRequest(
                f"unsupported Kubernetes quantity suffix: {suffix}"
            )
    return result

def subtract(
    available: dict[str, int],
    request: dict[str, int],
) -> dict[str, int]:
    """返回扣除一次 Pod 请求后的新资源字典，不修改传入值。"""

    result = dict(available)
    for name, value in request.items():
        result[name] = max(0, result.get(name, 0) - value)
    return result
