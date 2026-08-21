#!/usr/bin/env python3
"""严格模拟 Prometheus instant-query API、Bearer 认证、查询门闩和审计日志。"""

from __future__ import annotations

import json
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any


class PrometheusState:
    def __init__(self, metrics: dict[str, dict[str, float]], catalogue: dict[str, Any], token: str, log_path: Path | None) -> None:
        self.metrics = metrics
        self.token = token
        self.log_path = log_path
        self.allowed = threading.Event()
        self.query_to_name = {item["query"]: item["name"] for item in catalogue["metrics"]}
        self.node_label = catalogue.get("nodeLabel", "node")
        self.lock = threading.Lock()

    def log(self, item: dict[str, Any]) -> None:
        # timing-run 传入 None，业务请求期间不执行任何审计文件 I/O。
        if self.log_path is None:
            return
        item["timestampUnix"] = time.time()
        with self.lock:
            self.log_path.parent.mkdir(parents=True, exist_ok=True)
            with self.log_path.open("a", encoding="utf-8") as stream:
                stream.write(json.dumps(item, ensure_ascii=False) + "\n")


def handler_for(state: PrometheusState) -> type[BaseHTTPRequestHandler]:
    class Handler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:
            """测试驱动器通过控制端点精确打开/关闭 cold-cache 查询门闩。"""

            if self.path == "/control/open":
                state.allowed.set()
                state.log({"method": "POST", "path": self.path, "status": 200, "control": "open"})
                self._json(200, {"status": "open"})
                return
            if self.path == "/control/close":
                state.allowed.clear()
                state.log({"method": "POST", "path": self.path, "status": 200, "control": "closed"})
                self._json(200, {"status": "closed"})
                return
            self._json(404, {"status": "error", "error": "not found"})

        def do_GET(self) -> None:
            parsed = urllib.parse.urlparse(self.path)
            if parsed.path == "/-/ready":
                self._json(200, {"status": "ready"})
                return
            if parsed.path != "/api/v1/query":
                self._json(404, {"status": "error", "error": "not found"})
                return
            auth = self.headers.get("Authorization", "")
            if not auth:
                state.log({"method": "GET", "path": parsed.path, "status": 401, "auth": "missing"})
                self._json(401, {"status": "error", "errorType": "unauthorized", "error": "missing bearer token"})
                return
            if auth != "Bearer " + state.token:
                state.log({"method": "GET", "path": parsed.path, "status": 403, "auth": "invalid"})
                self._json(403, {"status": "error", "errorType": "forbidden", "error": "invalid bearer token"})
                return
            query = urllib.parse.parse_qs(parsed.query).get("query", [""])[0]
            metric_name = state.query_to_name.get(query)
            if metric_name is None:
                state.log({"method": "GET", "path": parsed.path, "status": 422, "auth": "valid", "queryMatched": False})
                self._json(422, {"status": "error", "errorType": "bad_data", "error": "unknown PromQL"})
                return
            state.allowed.wait(timeout=60)
            if not state.allowed.is_set():
                state.log({"method": "GET", "path": parsed.path, "status": 504, "auth": "valid", "metric": metric_name})
                self._json(504, {"status": "error", "errorType": "timeout", "error": "test gate closed"})
                return
            timestamp = int(time.time())
            result = [
                {"metric": {state.node_label: name}, "value": [timestamp, str(values[metric_name])]}
                for name, values in sorted(state.metrics.items())
            ]
            state.log({"method": "GET", "path": parsed.path, "status": 200, "auth": "valid", "metric": metric_name, "series": len(result), "queryMatched": True})
            self._json(200, {"status": "success", "data": {"resultType": "vector", "result": result}})

        def _json(self, status: int, value: dict[str, Any]) -> None:
            body = json.dumps(value, separators=(",", ":")).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            try:
                self.wfile.write(body)
            except (BrokenPipeError, ConnectionResetError):
                # timeout 用例会主动断开；Mock 仍保留请求审计，不打印线程栈。
                pass

        def log_message(self, _format: str, *args: Any) -> None:
            del args

    return Handler


def start(metrics: dict[str, dict[str, float]], catalogue: dict[str, Any], token: str, log_path: Path | None = None) -> tuple[ThreadingHTTPServer, PrometheusState]:
    state = PrometheusState(metrics, catalogue, token, log_path)
    server = ThreadingHTTPServer(("0.0.0.0", 0), handler_for(state))
    threading.Thread(target=server.serve_forever, name="mock-prometheus", daemon=True).start()
    return server, state
