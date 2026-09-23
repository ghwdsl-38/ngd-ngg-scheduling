import unittest

from algorithm_worker.algorithms.topology import TopologyAlgorithm
from algorithm_worker.context import AllocationContext, StaticNodeSnapshot


def context(mode: str, constraints: dict[str, str]) -> AllocationContext:
    nodes = [
        {
            "nodeName": "node-a",
            "nodeUID": "uid-a",
            "topology": {
                "leafDomainId": "leaf-a",
                "spineDomainId": "spine-a",
                "borderDomainId": "border-a",
                "uplinkDomainId": "uplink-a",
                "roomId": "room-a",
                "dataCenterId": "dc-a",
            },
        }
    ]
    return AllocationContext(
        request={"topologyMode": mode, "topologyConstraints": constraints},
        static_snapshot=StaticNodeSnapshot("s", "c", "t", tuple(nodes)),
        metric_snapshot=None,
        metrics_degraded=False,
        warnings=[],
        pod_minimums=[],
        current_nodes=nodes,
    )


class TopologyModeTests(unittest.TestCase):
    def test_compatible_mode_groups_by_uplink(self) -> None:
        result = TopologyAlgorithm().execute(
            context("uplink-compatible", {"uplinkDomain": "requiredSame"}), {}
        )
        self.assertEqual(
            [item["topologyLevel"] for item in result["node_groups"]],
            ["leafDomain", "uplinkDomain"],
        )

    def test_layered_mode_keeps_spine_and_border(self) -> None:
        result = TopologyAlgorithm().execute(
            context("layered", {"borderDomain": "requiredSame"}), {}
        )
        self.assertEqual(
            [item["topologyLevel"] for item in result["node_groups"]],
            ["leafDomain", "spineDomain", "borderDomain"],
        )

    def test_concrete_spine_is_filter_boundary_in_compatible_mode(self) -> None:
        result = TopologyAlgorithm().execute(
            context("uplink-compatible", {"spineDomain": "spine-a"}), {}
        )
        self.assertEqual(
            [item["topologyLevel"] for item in result["node_groups"]],
            ["leafDomain", "uplinkDomain"],
        )


if __name__ == "__main__":
    unittest.main()
