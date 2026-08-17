import unittest

from algorithm_api_server.pipeline import AlgorithmService as AlgorithmEngine
from algorithm_api_server.cache.static_nodes import canonical_hash
from algorithm_api_server.cache.metrics import MetricsCache


def static_node(name, switch_id, bandwidth, latency):
    return {
        "nodeName": name,
        "nodeUID": f"uid-{name}",
        "allocatable": {"cpu": "4", "memory": "8Gi"},
        "labels": {"demo.ngg/worker": "true"},
        "topology": {
            "switchId": switch_id,
            "coreSwitchId": "core-0",
            "bandwidthGbps": bandwidth,
            "latencyMillis": latency,
        },
    }


class AlgorithmTests(unittest.TestCase):
    def setUp(self):
        self.engine = AlgorithmEngine()
        nodes = []
        for switch_id, count, bandwidth, latency in (
            ("switch-a", 3, 20, 1.5),
            ("switch-b", 2, 10, 5),
            ("switch-c", 4, 25, 1),
        ):
            for index in range(count):
                nodes.append(
                    static_node(
                        f"{switch_id}-node-{index + 1}",
                        switch_id,
                        bandwidth,
                        latency,
                    )
                )
        self.content = {
            "clusterId": "demo",
            "topologyVersion": "three-switch-v1",
            "nodes": nodes,
        }
        self.snapshot_id = canonical_hash(self.content)
        self.engine.put_static_snapshot(self.snapshot_id, self.content)

    def request(self):
        return {
            "requestId": "request-1",
            "taskUID": "task-1",
            "ngdUID": "ngd-1",
            "ngdGeneration": 1,
            "nodeStaticSnapshotId": self.snapshot_id,
            "schedulerStateSnapshotId": "sha256:dynamic",
            "podSets": [
                {
                    "name": "worker",
                    "replicas": 4,
                    "minAvailable": 4,
                    "resourcesPerPod": {"cpu": "100m", "memory": "64Mi"},
                }
            ],
            "nodeRequirements": {"nodeSelector": {"demo.ngg/worker": "true"}},
            "maxCandidateGroups": 3,
            "schedulerState": [
                {
                    "nodeUID": node["nodeUID"],
                    "ready": True,
                    "unschedulable": False,
                    "requestedResources": {"cpu": "0", "memory": "0"},
                }
                for node in self.content["nodes"]
            ],
        }

    def test_returns_three_groups_in_stable_score_order(self):
        response = self.engine.calculate(self.request())
        self.assertEqual(response["status"], "SUCCESS")
        groups = response["candidateNodeGroups"]
        self.assertEqual(len(groups), 3)
        self.assertEqual([item["rank"] for item in groups], [1, 2, 3])
        self.assertEqual([item["groupId"] for item in groups], ["switch-c", "switch-a", "switch-b"])
        self.assertGreater(groups[0]["groupScore"], groups[1]["groupScore"])

    def test_hard_caps_candidate_groups_at_three(self):
        request = self.request()
        request["maxCandidateGroups"] = 4
        with self.assertRaisesRegex(ValueError, "between 1 and 3"):
            self.engine.calculate(request)

    def test_unready_switch_is_filtered(self):
        request = self.request()
        for state in request["schedulerState"]:
            if state["nodeUID"].startswith("uid-switch-c"):
                state["ready"] = False
        groups = self.engine.calculate(request)["candidateNodeGroups"]
        self.assertEqual([item["groupId"] for item in groups], ["switch-a", "switch-b"])

    def test_prometheus_cache_influences_soft_score_without_changing_hard_filter(self):
        results = {
            "cpu": [
                {"metric": {"node": node["nodeName"]}, "value": [1, "0.9"]}
                for node in self.content["nodes"]
                if node["nodeName"].startswith("switch-c")
            ],
            "memory": [
                {"metric": {"node": node["nodeName"]}, "value": [1, "0.9"]}
                for node in self.content["nodes"]
                if node["nodeName"].startswith("switch-c")
            ],
        }
        cache = MetricsCache(
            prometheus_url="http://prometheus",
            cpu_query="cpu",
            memory_query="memory",
            query=lambda query: results[query],
        )
        snapshot = cache.refresh()
        engine = AlgorithmEngine(metrics_cache=cache)
        engine.put_static_snapshot(self.snapshot_id, self.content)
        response = engine.calculate(self.request())
        self.assertEqual(response["metricSnapshotId"], snapshot.snapshot_id)
        self.assertFalse(response["degraded"])
        self.assertEqual(response["candidateNodeGroups"][0]["groupId"], "switch-a")

    def test_metrics_cache_keeps_last_good_snapshot_when_refresh_fails(self):
        calls = 0

        def query(_: str):
            nonlocal calls
            calls += 1
            if calls > 2:
                raise RuntimeError("prometheus unavailable")
            return [{"metric": {"node": "worker-1"}, "value": [1, "0.25"]}]

        cache = MetricsCache(
            prometheus_url="http://prometheus",
            cpu_query="cpu",
            memory_query="memory",
            query=query,
        )
        first = cache.refresh()
        with self.assertRaisesRegex(RuntimeError, "unavailable"):
            cache.refresh()
        current, degraded, _ = cache.for_calculation()
        self.assertEqual(current.snapshot_id, first.snapshot_id)
        self.assertFalse(degraded)


if __name__ == "__main__":
    unittest.main()
