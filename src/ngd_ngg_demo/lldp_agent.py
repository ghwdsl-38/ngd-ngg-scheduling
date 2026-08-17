from __future__ import annotations

import argparse
import json
import logging
import os
import time
from datetime import datetime, timezone
from typing import Any

from .kube import ApiError, GROUP, VERSION, KubeClient
from .lldp import receive_neighbor


LOG = logging.getLogger(__name__)
AGENT_VERSION = "v0.1.0"


def utc_timestamp() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds").replace(
        "+00:00", "Z"
    )


def _float_label(labels: dict[str, str], key: str) -> float:
    return float(labels.get(key, "0") or 0)


def simulated_observation(node: dict[str, Any]) -> dict[str, Any]:
    metadata = node.get("metadata", {})
    labels = metadata.get("labels", {})
    annotations = metadata.get("annotations", {})
    switch_id = labels.get("topology.demo.ngg.io/switch", "")
    if not switch_id:
        raise RuntimeError("simulated LLDP switch label is missing")
    return {
        "switchId": switch_id,
        "coreSwitchId": labels.get("topology.demo.ngg.io/core-switch", "core-0"),
        "bandwidthGbps": _float_label(
            labels, "topology.demo.ngg.io/bandwidth-gbps"
        ),
        "latencyMillis": _float_label(
            labels, "topology.demo.ngg.io/latency-ms"
        ),
        "localInterface": labels.get(
            "topology.demo.ngg.io/local-interface", "eth0"
        ),
        "remotePortId": annotations.get("topology.demo.ngg.io/remote-port", ""),
        "source": "SimulatedLLDP",
        "topologyVersion": "lldp-agent-simulated-v1",
    }


def real_lldp_observation(
    node: dict[str, Any], interfaces: set[str] | None, timeout_seconds: float
) -> dict[str, Any] | None:
    neighbor = receive_neighbor(interfaces, timeout_seconds)
    if not neighbor:
        return None
    labels = node.get("metadata", {}).get("labels", {})
    return {
        "switchId": neighbor.switch_id,
        "coreSwitchId": labels.get("topology.demo.ngg.io/core-switch", ""),
        # LLDP identifies the neighbor and port. Capacity/latency are enriched
        # from interface inventory or metrics labels in this demo contract.
        "bandwidthGbps": _float_label(
            labels, "topology.demo.ngg.io/bandwidth-gbps"
        ),
        "latencyMillis": _float_label(
            labels, "topology.demo.ngg.io/latency-ms"
        ),
        "localInterface": neighbor.local_interface,
        "remotePortId": neighbor.port_id,
        "source": "LLDP",
        "topologyVersion": "lldp-agent-real-v1",
    }


class LLDPAgent:
    def __init__(
        self,
        client: KubeClient,
        node_name: str,
        mode: str,
        interfaces: set[str] | None = None,
        listen_seconds: float = 35.0,
    ) -> None:
        if mode not in {"Simulated", "LLDP"}:
            raise ValueError(f"unsupported collection mode {mode!r}")
        self.client = client
        self.node_name = node_name
        self.mode = mode
        self.interfaces = interfaces
        self.listen_seconds = listen_seconds

    def reconcile(self) -> bool:
        node = self.client.get_node(self.node_name)
        if self.mode == "Simulated":
            observation = simulated_observation(node)
        else:
            observation = real_lldp_observation(
                node, self.interfaces, self.listen_seconds
            )
            if not observation:
                LOG.warning("node=%s no LLDP neighbor observed", self.node_name)
                return False

        node_uid = str(node.get("metadata", {}).get("uid", ""))
        desired_spec = {
            "nodeRef": {"name": self.node_name, "uid": node_uid},
            "collectionMode": self.mode,
        }
        try:
            topology = self.client.get_topology(self.node_name)
            if topology.get("spec") != desired_spec:
                self.client.patch_topology(self.node_name, {"spec": desired_spec})
        except ApiError as exc:
            if exc.status != 404:
                raise
            body = {
                "apiVersion": f"{GROUP}/{VERSION}",
                "kind": "NodeNetworkTopology",
                "metadata": {"name": self.node_name},
                "spec": desired_spec,
            }
            try:
                self.client.create_topology(body)
            except ApiError as create_exc:
                if create_exc.status != 409:
                    raise

        status = dict(observation)
        status["agentVersion"] = AGENT_VERSION
        status["observedAt"] = utc_timestamp()
        self.client.patch_topology_status(self.node_name, status)
        LOG.info(
            "node=%s mode=%s switch=%s port=%s",
            self.node_name,
            self.mode,
            status["switchId"],
            status["remotePortId"],
        )
        return True

    def run(self, interval: int) -> None:
        while True:
            try:
                self.reconcile()
            except Exception:
                LOG.exception("LLDP reconciliation failed for node=%s", self.node_name)
            time.sleep(interval)


def main() -> None:
    parser = argparse.ArgumentParser(description="NodeNetworkTopology LLDP Agent")
    parser.add_argument("--interval", type=int, default=30)
    args = parser.parse_args()
    logging.basicConfig(
        level=os.environ.get("LOG_LEVEL", "INFO"),
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    node_name = os.environ.get("NODE_NAME", "")
    if not node_name:
        raise RuntimeError("NODE_NAME is required")
    raw_interfaces = os.environ.get("LLDP_INTERFACES", "")
    interfaces = {item.strip() for item in raw_interfaces.split(",") if item.strip()}
    LLDPAgent(
        KubeClient(),
        node_name,
        os.environ.get("COLLECTION_MODE", "Simulated"),
        interfaces or None,
        float(os.environ.get("LLDP_LISTEN_SECONDS", "35")),
    ).run(args.interval)


if __name__ == "__main__":
    main()
