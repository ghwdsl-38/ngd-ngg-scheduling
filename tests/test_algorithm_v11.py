import unittest

from algorithm_api_server.pipeline import AlgorithmService as AlgorithmEngine
from algorithm_api_server.errors import (
    InvalidAlgorithmOrder,
    RequiredMetricsNotReady,
    StaticSnapshotNotFound,
)
from algorithm_api_server.cache.metrics import MetricsCache
from algorithm_api_server.cache.static_nodes import canonical_hash


def node(name, leaf, core="core-1", bandwidth=10, latency=5):
    return {
        "nodeName": name,
        "nodeUID": f"uid-{name}",
        "allocatable": {"cpu": "4", "memory": "8Gi"},
        "labels": {"demo.ngg/worker": "true"},
        "topology": {
            "coreSwitchId": core,
            "leafSwitchId": leaf,
            "bandwidthGbps": bandwidth,
            "latencyMillis": latency,
        },
    }


def static_content():
    nodes = []
    for leaf, count, bandwidth, latency in (
        ("switch-a", 3, 20, 1.5),
        ("switch-b", 2, 10, 5),
        ("switch-c", 4, 25, 1),
    ):
        for index in range(count):
            nodes.append(
                node(
                    f"{leaf}-node-{index + 1}",
                    leaf,
                    bandwidth=bandwidth,
                    latency=latency,
                )
            )
    return {
        "clusterId": "demo",
        "topologyVersion": "core-leaf-node-v1",
        "nodes": nodes,
    }


class AlgorithmV11Tests(unittest.TestCase):
    def setUp(self):
        self.engine = AlgorithmEngine()
        self.static = static_content()
        self.static_id = canonical_hash(self.static)
        self.engine.put_static_snapshot(self.static_id, self.static)

    def request(self):
        return {
            "requestId": "request-1",
            "taskUID": "task-1",
            "ngdUID": "ngd-1",
            "ngdGeneration": 1,
            "nodeStaticSnapshotId": self.static_id,
            "podSets": [
                {
                    "name": "worker",
                    "replicas": 4,
                    "minAvailable": 4,
                    "resourcesPerPod": {"cpu": "100m", "memory": "64Mi"},
                }
            ],
            "nodeRequirements": {
                "nodeSelector": {"demo.ngg/worker": "true"}
            },
            "nodeUsageStates": [
                {"nodeUID": item["nodeUID"], "inUse": False}
                for item in self.static["nodes"]
            ],
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
                    "parameters": {"profile": "balanced-v1"},
                },
            ],
            "maxCandidateGroups": 3,
        }

    def test_ready_does_not_depend_on_caches(self):
        empty = AlgorithmEngine()
        self.assertEqual(empty.ready()["status"], "ready")
        self.assertFalse(empty.cache_status()["nodeStatic"]["ready"])

    def test_static_cache_is_hash_addressed_and_keeps_previous(self):
        ack = self.engine.put_static_snapshot(self.static_id, self.static)
        self.assertEqual(ack["snapshotId"], self.static_id)
        changed = static_content()
        changed["nodes"][0]["labels"]["revision"] = "2"
        changed_id = canonical_hash(changed)
        self.engine.put_static_snapshot(changed_id, changed)
        status = self.engine.cache_status()["nodeStatic"]
        self.assertEqual(status["currentSnapshotId"], changed_id)
        self.assertEqual(status["previousSnapshotId"], self.static_id)
        self.assertEqual(
            self.engine.allocate(self.request())["nodeStaticSnapshotId"],
            self.static_id,
        )

    def test_usage_state_is_request_scoped(self):
        first = self.request()
        for state in first["nodeUsageStates"]:
            if state["nodeUID"].startswith("uid-switch-c"):
                state["inUse"] = True
        groups = self.engine.allocate(first)["candidateNodeGroups"]
        self.assertNotIn("leaf:switch-c", [group["groupId"] for group in groups])
        groups = self.engine.allocate(self.request())["candidateNodeGroups"]
        self.assertIn("leaf:switch-c", [group["groupId"] for group in groups])

    def test_pipeline_rejects_stage_regression(self):
        request = self.request()
        request["algorithms"] = [
            {"name": "topology", "version": "v1"},
            {"name": "requirement", "version": "v1"},
            {"name": "loadbalance", "version": "v1"},
        ]
        with self.assertRaises(InvalidAlgorithmOrder):
            self.engine.allocate(request)

    def test_topology_falls_back_to_core(self):
        request = self.request()
        request["algorithms"][1]["parameters"]["requiredDistinctNodes"] = 5
        groups = self.engine.allocate(request)["candidateNodeGroups"]
        self.assertEqual(len(groups), 1)
        self.assertEqual(groups[0]["topologyLevel"], "coreSwitch")
        self.assertEqual(groups[0]["groupId"], "core:core-1")

    def test_response_is_stable_top_three(self):
        response = self.engine.allocate(self.request())
        self.assertEqual(response["status"], "SUCCESS")
        self.assertEqual(
            [group["rank"] for group in response["candidateNodeGroups"]],
            [1, 2, 3],
        )
        self.assertEqual(
            [group["groupId"] for group in response["candidateNodeGroups"]],
            ["leaf:switch-c", "leaf:switch-a", "leaf:switch-b"],
        )
        self.assertIn("metricsSnapshotId", response)
        self.assertNotIn("schedulerStateSnapshotId", response)

    def test_required_metrics_fail_closed(self):
        request = self.request()
        request["algorithms"][2]["parameters"]["requireMetrics"] = True
        with self.assertRaises(RequiredMetricsNotReady):
            self.engine.allocate(request)

    def test_unknown_static_hash_is_retryable(self):
        request = self.request()
        request["nodeStaticSnapshotId"] = "sha256:" + "0" * 64
        with self.assertRaises(StaticSnapshotNotFound) as caught:
            self.engine.allocate(request)
        self.assertEqual(caught.exception.status_code, 409)
        self.assertTrue(caught.exception.retryable)

    def test_prometheus_cache_affects_scoring(self):
        values = {
            "cpu": [
                {"metric": {"node": item["nodeName"]}, "value": [1, "0.9"]}
                for item in self.static["nodes"]
                if item["nodeName"].startswith("switch-c")
            ],
            "memory": [
                {"metric": {"node": item["nodeName"]}, "value": [1, "0.9"]}
                for item in self.static["nodes"]
                if item["nodeName"].startswith("switch-c")
            ],
        }
        cache = MetricsCache(
            prometheus_url="http://prometheus",
            cpu_query="cpu",
            memory_query="memory",
            query=lambda query: values[query],
        )
        metric_snapshot = cache.refresh()
        engine = AlgorithmEngine(metrics_cache=cache)
        engine.put_static_snapshot(self.static_id, self.static)
        response = engine.allocate(self.request())
        self.assertEqual(response["metricsSnapshotId"], metric_snapshot.snapshot_id)
        self.assertFalse(response["degraded"])
        self.assertEqual(response["candidateNodeGroups"][0]["groupId"], "leaf:switch-a")


if __name__ == "__main__":
    unittest.main()
