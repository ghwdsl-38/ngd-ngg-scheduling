import copy
import time
import unittest

from algorithm_worker.cache.metrics import MetricsCache
from algorithm_worker.cache.static_nodes import canonical_hash
from algorithm_worker.pipeline import AlgorithmService


NODE_COUNT = 1_000
CORE_COUNT = 4
LEAVES_PER_CORE = 25
NODES_PER_LEAF = 10
MAX_CALCULATION_SECONDS = 5.0


def build_fixture():
    nodes = []
    cpu_results = []
    memory_results = []
    usage_states = []
    for number in range(1, NODE_COUNT + 1):
        leaf_number = (number - 1) // NODES_PER_LEAF + 1
        core_number = (leaf_number - 1) // LEAVES_PER_CORE + 1
        position = (number - 1) % NODES_PER_LEAF + 1
        name = f"worker-{number:04d}"
        uid = f"uid-{name}"
        leaf = f"leaf-{leaf_number:03d}"
        core = f"core-{core_number:02d}"
        bandwidth, latency = (
            ((10, 5), (20, 2), (25, 1), (40, 0.5))[
                (leaf_number - 1) % 4
            ]
        )
        nodes.append(
            {
                "nodeName": name,
                "nodeUID": uid,
                "allocatable": {
                    "cpu": "64",
                    "memory": "256Gi",
                    "nvidia.com/gpu": "8",
                },
                "labels": {
                    "demo.ngg/worker": "true",
                    "demo.ngg/leaf": leaf,
                },
                "topology": {
                    "coreSwitchId": core,
                    "leafSwitchId": leaf,
                    "bandwidthGbps": bandwidth,
                    "latencyMillis": latency,
                },
            }
        )
        cpu = 0.08 + ((leaf_number * 13 + position * 7) % 65) / 100
        memory = 0.10 + ((leaf_number * 11 + position * 5) % 60) / 100
        cpu_results.append(
            {"metric": {"node": name}, "value": [1, str(min(cpu, 0.99))]}
        )
        memory_results.append(
            {"metric": {"node": name}, "value": [1, str(min(memory, 0.99))]}
        )
        usage_states.append({"nodeUID": uid, "inUse": position in {5, 10}})

    static = {
        "clusterId": "algorithm-1000-node-unit-test",
        "topologyVersion": "core-leaf-node-v1",
        "nodes": nodes,
    }
    metrics = {"cpu": cpu_results, "memory": memory_results}
    return static, metrics, usage_states


class AlgorithmThousandNodeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.static, metric_results, cls.usage_states = build_fixture()
        cls.static_id = canonical_hash(cls.static)
        metrics_cache = MetricsCache(
            prometheus_url="http://mock-prometheus",
            cpu_query="cpu",
            memory_query="memory",
            query=lambda query: metric_results[query],
        )
        cls.metric_snapshot = metrics_cache.refresh()
        cls.service = AlgorithmService(metrics_cache=metrics_cache)
        cls.ack = cls.service.put_static_snapshot(cls.static_id, cls.static)

    @classmethod
    def request(cls):
        return {
            "requestId": "algorithm-1000-unit-request-1",
            "taskUID": "task-algorithm-1000",
            "ngdUID": "ngd-algorithm-1000",
            "ngdGeneration": 1,
            "nodeStaticSnapshotId": cls.static_id,
            "podSets": [
                {
                    "name": "worker",
                    "replicas": 32,
                    "minAvailable": 32,
                    "resourcesPerPod": {
                        "cpu": "2",
                        "memory": "4Gi",
                        "nvidia.com/gpu": "1",
                    },
                }
            ],
            "nodeRequirements": {
                "nodeSelector": {"demo.ngg/worker": "true"}
            },
            "nodeUsageStates": copy.deepcopy(cls.usage_states),
            "algorithms": [
                {"name": "requirement", "version": "v1"},
                {
                    "name": "topology",
                    "version": "v1",
                    "parameters": {
                        "strategy": "NarrowestFit",
                        "widestAllowedLevel": "coreSwitch",
                    },
                },
                {
                    "name": "loadbalance",
                    "version": "v1",
                    "parameters": {
                        "profile": "balanced-v1",
                        "requireMetrics": True,
                    },
                },
            ],
            "maxCandidateGroups": 3,
        }

    def test_1000_node_cache_pipeline_and_stable_top_three(self):
        self.assertEqual(self.ack["nodeCount"], NODE_COUNT)
        status = self.service.cache_status()
        self.assertEqual(status["nodeStatic"]["nodeCount"], NODE_COUNT)
        self.assertEqual(status["metrics"]["nodeCount"], NODE_COUNT)
        self.assertFalse(status["metrics"]["degraded"])

        request = self.request()
        started = time.perf_counter()
        first = self.service.allocate(request)
        elapsed = time.perf_counter() - started
        second = self.service.allocate(copy.deepcopy(request))

        self.assertLess(elapsed, MAX_CALCULATION_SECONDS)
        self.assertEqual(first["status"], "SUCCESS")
        self.assertEqual(first["metricsSnapshotId"], self.metric_snapshot.snapshot_id)
        self.assertEqual(len(first["candidateNodeGroups"]), 3)
        self.assertEqual(
            first["candidateNodeGroups"], second["candidateNodeGroups"]
        )
        self.assertEqual(
            [group["rank"] for group in first["candidateNodeGroups"]],
            [1, 2, 3],
        )
        self.assertTrue(
            all(
                left["groupScore"] >= right["groupScore"]
                for left, right in zip(
                    first["candidateNodeGroups"],
                    first["candidateNodeGroups"][1:],
                )
            )
        )
        returned_uids = [
            node["nodeUID"]
            for group in first["candidateNodeGroups"]
            for node in group["nodes"]
        ]
        self.assertEqual(len(returned_uids), len(set(returned_uids)))

    def test_1000_node_dynamic_state_is_full_and_request_scoped(self):
        request = self.request()
        self.assertEqual(len(request["nodeUsageStates"]), NODE_COUNT)
        self.assertEqual(
            sum(item["inUse"] for item in request["nodeUsageStates"]),
            200,
        )
        first = self.service.allocate(request)
        removed = first["candidateNodeGroups"][0]
        busy = {node["nodeUID"] for node in removed["nodes"]}

        changed = self.request()
        changed["requestId"] = "algorithm-1000-unit-request-2"
        for state in changed["nodeUsageStates"]:
            if state["nodeUID"] in busy:
                state["inUse"] = True
        second = self.service.allocate(changed)
        self.assertNotIn(
            removed["groupId"],
            [group["groupId"] for group in second["candidateNodeGroups"]],
        )

        third = self.service.allocate(self.request())
        self.assertEqual(
            removed["groupId"], third["candidateNodeGroups"][0]["groupId"]
        )


if __name__ == "__main__":
    unittest.main()
