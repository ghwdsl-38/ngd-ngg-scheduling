from __future__ import annotations

import json
import urllib.error
import urllib.parse
import urllib.request
from typing import Any


class AlgorithmClientError(RuntimeError):
    def __init__(self, status: int, message: str):
        super().__init__(f"Algorithm API returned HTTP {status}: {message}")
        self.status = status


class AlgorithmClient:
    def __init__(self, base_url: str, timeout_seconds: float = 5.0) -> None:
        self.base_url = base_url.rstrip("/")
        self.timeout_seconds = timeout_seconds

    def request(
        self, method: str, path: str, body: dict[str, Any] | None = None
    ) -> dict[str, Any]:
        data = None if body is None else json.dumps(body).encode("utf-8")
        request = urllib.request.Request(
            self.base_url + path,
            data=data,
            method=method,
            headers={"Accept": "application/json", "Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(
                request, timeout=self.timeout_seconds
            ) as response:
                raw = response.read()
                return json.loads(raw) if raw else {}
        except urllib.error.HTTPError as exc:
            message = exc.read().decode(errors="replace")
            raise AlgorithmClientError(exc.code, message) from exc
        except urllib.error.URLError as exc:
            raise AlgorithmClientError(503, str(exc.reason)) from exc

    def sync_static_snapshot(
        self, snapshot_id: str, snapshot: dict[str, Any]
    ) -> dict[str, Any]:
        quoted = urllib.parse.quote(snapshot_id, safe=":")
        return self.request(
            "PUT", f"/internal/v1/node-static-snapshots/{quoted}", snapshot
        )

    def calculate(self, request: dict[str, Any]) -> dict[str, Any]:
        return self.request("POST", "/api/v1/node-groups/calculate", request)

