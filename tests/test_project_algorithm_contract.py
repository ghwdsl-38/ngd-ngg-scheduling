import unittest

from algorithm_worker.pipeline import AlgorithmService as AlgorithmEngine
from algorithm_worker.cache.static_nodes import canonical_hash


def legacy_prc_static_snapshot():
    nodes = []
    for switch_id, count in (("switch-a", 3), ("switch-b", 2), ("switch-c", 4)):
        for index in range(count):
            name = f"{switch_id}-node-{index + 1}"
            nodes.append(
                {
                    "nodeName": name,
                    "nodeUID": f"uid-{name}",
                    "createdAt": "2026-08-16T00:00:00Z",
                    "allocatable": {"cpu": "4", "memory": "8Gi"},
                    "labels": {"demo.ngg/worker": "true"},
                    "topology": {
                        "switchId": switch_id,
                        "coreSwitchId": "core-0",
                        "bandwidthGbps": 25 if switch_id == "switch-c" else 10,
                        "latencyMillis": 1 if switch_id == "switch-c" else 5,
                    },
                }
            )
    return {
        "clusterId": "demo-cluster",
        "topologyVersion": "demo-v1",
        "nodes": nodes,
    }


class CurrentProjectContractTests(unittest.TestCase):
    def setUp(self):
        self.engine = AlgorithmEngine()
        self.static = legacy_prc_static_snapshot()
        self.static_id = canonical_hash(self.static)
        self.ack = self.engine.put_static_snapshot(self.static_id, self.static)

    def legacy_request(self):
        scheduler_state = [
            {
                "nodeUID": node["nodeUID"],
                "ready": True,
                "unschedulable": False,
                "requestedResources": {"cpu": "50m", "memory": "32Mi"},
            }
            for node in self.static["nodes"]
        ]
        return {
            "requestId": "ngd-generation-1-state-abcd",
            "taskUID": "task-uid",
            "ngdUID": "ngd-uid",
            "ngdGeneration": 1,
            "nodeStaticSnapshotId": self.static_id,
            "schedulerStateSnapshotId": "sha256:legacy-state",
            "schedulerStateCapturedAt": "2026-08-16T00:00:05Z",
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
            "topologyRequirement": {"level": "leafGroup", "mode": "same"},
            "algorithms": [
                {"name": "requirement", "version": "v1"},
                {"name": "topology", "version": "v1"},
                {"name": "loadbalance", "version": "v1"},
            ],
            "maxCandidateGroups": 3,
            "schedulerState": scheduler_state,
        }

    def test_static_ack_matches_current_go_prc(self):
        self.assertEqual(self.ack["algorithmBootId"], self.engine.boot_id)
        self.assertEqual(self.ack["acceptedSnapshotId"], self.static_id)
        self.assertEqual(self.ack["nodeCount"], 9)

    def test_legacy_response_matches_current_prc_and_ngg_shape(self):
        request = self.legacy_request()
        response = self.engine.calculate(request)
        for field in ("requestId", "taskUID", "ngdUID", "ngdGeneration"):
            self.assertEqual(response[field], request[field])
        self.assertEqual(response["algorithmBootId"], self.ack["algorithmBootId"])
        self.assertEqual(response["nodeStaticSnapshotId"], self.static_id)
        self.assertEqual(
            response["schedulerStateSnapshotId"],
            request["schedulerStateSnapshotId"],
        )
        self.assertIn("metricSnapshotId", response)
        groups = response["candidateNodeGroups"]
        self.assertLessEqual(len(groups), 3)
        self.assertEqual(
            [group["rank"] for group in groups],
            list(range(1, len(groups) + 1)),
        )
        self.assertTrue(
            all(group["topologyLevel"] == "leafGroup" for group in groups)
        )
        self.assertTrue(
            all(
                groups[index - 1]["groupScore"] >= groups[index]["groupScore"]
                for index in range(1, len(groups))
            )
        )
        node_uids = [
            node["nodeUID"]
            for group in groups
            for node in group["nodes"]
        ]
        self.assertEqual(len(node_uids), len(set(node_uids)))

    def test_new_and_legacy_contract_share_static_cache(self):
        legacy = self.legacy_request()
        modern = {
            key: value
            for key, value in legacy.items()
            if key not in {
                "schedulerState",
                "schedulerStateSnapshotId",
                "schedulerStateCapturedAt",
                "topologyRequirement",
            }
        }
        modern["nodeUsageStates"] = [
            {"nodeUID": node["nodeUID"], "inUse": False}
            for node in self.static["nodes"]
        ]
        response = self.engine.allocate(modern)
        self.assertEqual(response["nodeStaticSnapshotId"], self.static_id)
        self.assertTrue(
            all(
                group["topologyLevel"] in {"leafSwitch", "coreSwitch"}
                for group in response["candidateNodeGroups"]
            )
        )


if __name__ == "__main__":
    unittest.main()
