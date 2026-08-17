import unittest

from algorithm_worker.cache.static_nodes import canonical_hash
from algorithm_worker.worker import AlgorithmWorker


class AlgorithmWorkerTests(unittest.TestCase):
    def test_worker_receives_resolved_data_without_owning_caches(self):
        nodes = [
            {
                "nodeName": f"worker-{index}",
                "nodeUID": f"uid-{index}",
                "allocatable": {"cpu": "4", "memory": "8Gi"},
                "labels": {"demo.ngg/worker": "true"},
                "topology": {
                    "coreSwitchId": "core-0",
                    "leafSwitchId": "switch-a",
                    "bandwidthGbps": 20,
                    "latencyMillis": 1,
                },
            }
            for index in range(3)
        ]
        static = {
            "clusterId": "worker-test",
            "topologyVersion": "v1",
            "nodes": nodes,
        }
        request = {
            "requestId": "request-worker-test",
            "taskUID": "task-1",
            "ngdUID": "ngd-1",
            "ngdGeneration": 1,
            "nodeStaticSnapshotId": canonical_hash(static),
            "podSets": [
                {
                    "name": "worker",
                    "minAvailable": 2,
                    "resourcesPerPod": {"cpu": "100m", "memory": "64Mi"},
                }
            ],
            "nodeRequirements": {
                "nodeSelector": {"demo.ngg/worker": "true"}
            },
            "nodeUsageStates": [
                {"nodeUID": node["nodeUID"], "inUse": False}
                for node in nodes
            ],
            "maxCandidateGroups": 3,
        }
        response = AlgorithmWorker().calculate(
            {
                "request": request,
                "staticSnapshot": {
                    **static,
                    "snapshotId": request["nodeStaticSnapshotId"],
                },
                "metricSnapshot": None,
                "metricsDegraded": True,
                "warnings": ["Prometheus metrics cache is disabled"],
            }
        )
        groups = response["candidateNodeGroups"]
        self.assertEqual(len(groups), 1)
        self.assertEqual(groups[0]["groupId"], "leaf:switch-a")
        self.assertEqual(groups[0]["rank"], 1)


if __name__ == "__main__":
    unittest.main()
