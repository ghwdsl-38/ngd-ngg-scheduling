from __future__ import annotations

import json
import urllib.parse
import urllib.request
from typing import Any


class PrometheusCollector:
    def __init__(self, base_url: str, timeout_seconds: float = 5) -> None:
        self.base_url = base_url.rstrip("/")
        self.timeout_seconds = max(0.1, timeout_seconds)

    def query(self, promql: str) -> list[dict[str, Any]]:
        if not promql:
            return []
        params = urllib.parse.urlencode({"query": promql})
        request = urllib.request.Request(
            f"{self.base_url}/api/v1/query?{params}",
            headers={"Accept": "application/json"},
        )
        with urllib.request.urlopen(
            request,
            timeout=self.timeout_seconds,
        ) as response:
            payload = json.loads(response.read(4 << 20))
        if payload.get("status") != "success":
            raise RuntimeError(f"Prometheus query failed: {payload}")
        data = payload.get("data", {})
        if data.get("resultType") != "vector":
            raise RuntimeError("Prometheus instant query did not return a vector")
        return data.get("result", [])
