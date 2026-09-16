import unittest

from algorithm_worker.worker import AlgorithmWorker


GIB = 1024**3


def node(name: str, cpu_milli: int, memory_bytes: int) -> dict:
    return {
        "nodeUID": "uid-" + name,
        "nodeName": name,
        "labels": {
            "kubernetes.io/arch": "amd64",
            "kubernetes.io/hostname": name,
        },
        "topology": {
            "dataCenterId": "dc-1",
            "roomId": "room-1",
            "borderDomainId": "border-1",
            "leafDomainId": "leaf-1",
        },
        "normalizedAllocatableResources": {
            "cpu": str(cpu_milli),
            "memory": str(memory_bytes),
        },
    }


def payload(ngd: dict, nodes: list[dict]) -> dict:
    return {
        "request": {
            "requestId": "request-1",
            "requestMode": "resourcePool",
            "ngd": ngd,
            "nodeUsageStates": [
                {
                    "nodeUID": item["nodeUID"],
                    "inUse": False,
                    "normalizedRequestedResources": {},
                }
                for item in nodes
            ],
        },
        "staticSnapshot": {
            "snapshotId": "snapshot-1",
            "clusterId": "cluster-1",
            "topologyVersion": "v1",
            "nodes": nodes,
        },
        "metricsDegraded": False,
        "warnings": [],
    }


class WorkerResourceTests(unittest.TestCase):
    def setUp(self) -> None:
        self.worker = AlgorithmWorker()
        self.nodes = [
            node("master1", 101830, 447 * GIB),
            node("master3", 87430, 434 * GIB),
            node("master2", 71890, 419 * GIB),
        ]

    def test_selects_two_nodes_from_normalized_resources(self) -> None:
        ngd = {
            "maxNodes": 2,
            "nodeSelector": {"matchLabels": {"kubernetes.io/arch": "amd64"}},
            "normalizedResources": {
                "minResources": {"cpu": "150000", "memory": str(700 * GIB)},
                "quota": {"cpu": "220000", "memory": str(950 * GIB)},
            },
        }
        result = self.worker.calculate(payload(ngd, self.nodes))
        self.assertNotIn("failure", result)
        selected = result["candidateNodeGroups"][0]["nodes"]
        self.assertEqual(2, len(selected))
        self.assertIn("cpuMilli", selected[0]["resources"])
        self.assertIn("memoryBytes", selected[0]["resources"])

    def test_reports_node_selector_root_cause(self) -> None:
        ngd = {
            "maxNodes": 1,
            "nodeSelector": {"matchLabels": {"missing": "true"}},
            "normalizedResources": {
                "minResources": {"cpu": "1000", "memory": str(GIB)},
                "quota": {},
            },
        }
        result = self.worker.calculate(payload(ngd, self.nodes))
        self.assertEqual("NODE_SELECTOR_NO_MATCH", result["failure"]["code"])

    def test_reports_quota_root_cause(self) -> None:
        ngd = {
            "maxNodes": 3,
            "nodeSelector": {"matchLabels": {"kubernetes.io/arch": "amd64"}},
            "normalizedResources": {
                "minResources": {"cpu": "1000", "memory": str(GIB)},
                "quota": {"cpu": "80000", "memory": str(400 * GIB)},
            },
        }
        result = self.worker.calculate(payload(ngd, self.nodes))
        self.assertEqual("QUOTA_PREVENTS_MINIMUM", result["failure"]["code"])


if __name__ == "__main__":
    unittest.main()
