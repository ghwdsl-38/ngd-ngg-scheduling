import unittest

from ngd_ngg_demo.domain import (
    can_place_minimums,
    free_resources_by_node,
    parse_resources,
    select_candidate_nodes,
)


def node(name, partition, cpu="4", memory="8Gi", ready=True):
    return {
        "metadata": {"name": name, "uid": f"uid-{name}", "labels": {"demo.ngg/partition": partition}},
        "spec": {},
        "status": {
            "allocatable": {"cpu": cpu, "memory": memory},
            "conditions": [{"type": "Ready", "status": "True" if ready else "False"}],
        },
    }


class DomainTests(unittest.TestCase):
    def test_parse_resources(self):
        self.assertEqual(
            parse_resources({"cpu": "1500m", "memory": "2Gi", "nvidia.com/gpu": "2"}),
            {"cpu": 1500, "memory": 2 * 1024**3, "nvidia.com/gpu": 2},
        )

    def test_scheduled_pods_reduce_free_resources(self):
        nodes = [node("n1", "training")]
        pods = [{
            "spec": {
                "nodeName": "n1",
                "containers": [{"resources": {"requests": {"cpu": "500m", "memory": "1Gi"}}}],
            },
            "status": {"phase": "Running"},
        }]
        free = free_resources_by_node(nodes, pods)
        self.assertEqual(free["n1"]["cpu"], 3500)
        self.assertEqual(free["n1"]["memory"], 7 * 1024**3)

    def test_minimums_can_share_a_node(self):
        free = {"n1": {"cpu": 4000, "memory": 8 * 1024**3}}
        minimums = [("worker", 2, {"cpu": 1000, "memory": 1024**3})]
        self.assertTrue(can_place_minimums(["n1"], free, minimums))

    def test_heterogeneous_minimums_are_checked(self):
        free = {"n1": {"cpu": 4000, "memory": 8 * 1024**3}}
        minimums = [
            ("master", 1, {"cpu": 2000, "memory": 4 * 1024**3}),
            ("worker", 2, {"cpu": 1500, "memory": 2 * 1024**3}),
        ]
        self.assertFalse(can_place_minimums(["n1"], free, minimums))

    def test_selector_and_max_nodes_bound_candidates(self):
        nodes = [node("n1", "training"), node("n2", "training"), node("n3", "batch")]
        demand = {
            "podSets": [{
                "name": "worker",
                "replicas": 1,
                "minAvailable": 1,
                "resourcesPerPod": {"cpu": "100m", "memory": "64Mi"},
            }],
            "nodeRequirements": {"nodeSelector": {"demo.ngg/partition": "training"}},
            "candidatePolicy": {"maxNodes": 1},
        }
        candidates, satisfied = select_candidate_nodes(nodes, [], demand)
        self.assertTrue(satisfied)
        self.assertEqual(len(candidates), 1)
        self.assertIn(candidates[0]["name"], {"n1", "n2"})

    def test_not_ready_nodes_are_excluded(self):
        demand = {
            "podSets": [{
                "name": "worker",
                "replicas": 1,
                "minAvailable": 1,
                "resourcesPerPod": {"cpu": "100m"},
            }],
            "nodeRequirements": {"nodeSelector": {"demo.ngg/partition": "training"}},
        }
        candidates, satisfied = select_candidate_nodes([node("n1", "training", ready=False)], [], demand)
        self.assertFalse(satisfied)
        self.assertEqual(candidates, [])


if __name__ == "__main__":
    unittest.main()

