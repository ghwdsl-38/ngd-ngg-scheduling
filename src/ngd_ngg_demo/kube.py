from __future__ import annotations

import json
import os
import ssl
import urllib.error
import urllib.parse
import urllib.request
from typing import Any


GROUP = "scheduling.demo.ngg.io"
VERSION = "v1alpha1"
NGD_PATH = f"/apis/{GROUP}/{VERSION}/nodegroupdemands"
NGG_PATH = f"/apis/{GROUP}/{VERSION}/nodegroupgrants"
NNT_PATH = f"/apis/{GROUP}/{VERSION}/nodenetworktopologies"


class ApiError(RuntimeError):
    def __init__(self, status: int, message: str):
        super().__init__(f"Kubernetes API returned HTTP {status}: {message}")
        self.status = status


class KubeClient:
    def __init__(self) -> None:
        host = os.environ.get("KUBERNETES_SERVICE_HOST")
        port = os.environ.get("KUBERNETES_SERVICE_PORT_HTTPS", "443")
        if not host:
            raise RuntimeError("KUBERNETES_SERVICE_HOST is not set")
        self.base = f"https://{host}:{port}"
        token_path = "/var/run/secrets/kubernetes.io/serviceaccount/token"
        ca_path = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
        with open(token_path, encoding="utf-8") as handle:
            self.token = handle.read().strip()
        self.context = ssl.create_default_context(cafile=ca_path)

    def request(
        self,
        method: str,
        path: str,
        body: dict[str, Any] | None = None,
        content_type: str = "application/json",
    ) -> dict[str, Any]:
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(
            self.base + path,
            data=data,
            method=method,
            headers={
                "Authorization": f"Bearer {self.token}",
                "Accept": "application/json",
                "Content-Type": content_type,
            },
        )
        try:
            with urllib.request.urlopen(request, context=self.context, timeout=10) as response:
                raw = response.read()
                return json.loads(raw) if raw else {}
        except urllib.error.HTTPError as exc:
            message = exc.read().decode(errors="replace")
            raise ApiError(exc.code, message) from exc

    def list_nodes(self) -> list[dict[str, Any]]:
        return self.request("GET", "/api/v1/nodes").get("items", [])

    def get_node(self, name: str) -> dict[str, Any]:
        name_q = urllib.parse.quote(name, safe="")
        return self.request("GET", f"/api/v1/nodes/{name_q}")

    def list_pods(self) -> list[dict[str, Any]]:
        return self.request("GET", "/api/v1/pods").get("items", [])

    def list_demands(self) -> list[dict[str, Any]]:
        return self.request("GET", NGD_PATH).get("items", [])

    def list_grants(self) -> list[dict[str, Any]]:
        return self.request("GET", NGG_PATH).get("items", [])

    def list_topologies(self) -> list[dict[str, Any]]:
        return self.request("GET", NNT_PATH).get("items", [])

    def get_topology(self, name: str) -> dict[str, Any]:
        name_q = urllib.parse.quote(name, safe="")
        return self.request("GET", f"{NNT_PATH}/{name_q}")

    def create_topology(self, body: dict[str, Any]) -> dict[str, Any]:
        return self.request("POST", NNT_PATH, body)

    def patch_topology(self, name: str, body: dict[str, Any]) -> dict[str, Any]:
        name_q = urllib.parse.quote(name, safe="")
        return self.request(
            "PATCH",
            f"{NNT_PATH}/{name_q}",
            body,
            "application/merge-patch+json",
        )

    def patch_topology_status(
        self, name: str, status: dict[str, Any]
    ) -> dict[str, Any]:
        name_q = urllib.parse.quote(name, safe="")
        return self.request(
            "PATCH",
            f"{NNT_PATH}/{name_q}/status",
            {"status": status},
            "application/merge-patch+json",
        )

    def get_task(self, namespace: str, task_ref: dict[str, Any]) -> dict[str, Any]:
        api_version = task_ref["apiVersion"]
        kind = task_ref["kind"]
        plural_by_kind = {"Job": "jobs", "Deployment": "deployments"}
        plural = plural_by_kind.get(kind)
        if not plural:
            raise ValueError(f"unsupported task kind {kind!r}")
        namespace_q = urllib.parse.quote(namespace, safe="")
        name_q = urllib.parse.quote(task_ref["name"], safe="")
        if "/" in api_version:
            group, version = api_version.split("/", 1)
            path = f"/apis/{group}/{version}/namespaces/{namespace_q}/{plural}/{name_q}"
        else:
            path = f"/api/{api_version}/namespaces/{namespace_q}/{plural}/{name_q}"
        return self.request("GET", path)

    def create_grant(self, namespace: str, body: dict[str, Any]) -> dict[str, Any]:
        namespace_q = urllib.parse.quote(namespace, safe="")
        return self.request(
            "POST",
            f"/apis/{GROUP}/{VERSION}/namespaces/{namespace_q}/nodegroupgrants",
            body,
        )

    def update_grant(self, grant: dict[str, Any]) -> dict[str, Any]:
        namespace = urllib.parse.quote(grant["metadata"]["namespace"], safe="")
        name = urllib.parse.quote(grant["metadata"]["name"], safe="")
        return self.request(
            "PUT",
            f"/apis/{GROUP}/{VERSION}/namespaces/{namespace}/nodegroupgrants/{name}",
            grant,
        )

    def patch_grant_status(self, namespace: str, name: str, status: dict[str, Any]) -> None:
        namespace_q = urllib.parse.quote(namespace, safe="")
        name_q = urllib.parse.quote(name, safe="")
        self.request(
            "PATCH",
            f"/apis/{GROUP}/{VERSION}/namespaces/{namespace_q}/nodegroupgrants/{name_q}/status",
            {"status": status},
            "application/merge-patch+json",
        )

    def patch_demand_status(self, namespace: str, name: str, status: dict[str, Any]) -> None:
        namespace_q = urllib.parse.quote(namespace, safe="")
        name_q = urllib.parse.quote(name, safe="")
        self.request(
            "PATCH",
            f"/apis/{GROUP}/{VERSION}/namespaces/{namespace_q}/nodegroupdemands/{name_q}/status",
            {"status": status},
            "application/merge-patch+json",
        )
