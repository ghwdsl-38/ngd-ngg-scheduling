import copy
import unittest
from datetime import datetime, timezone

from ngd_ngg_demo.controller import PRCController


class FakeKube:
    def __init__(self):
        self.updated_grants = []
        self.grant_statuses = []
        self.demand_statuses = []

    def update_grant(self, grant):
        applied = copy.deepcopy(grant)
        applied["metadata"]["generation"] += 1
        self.updated_grants.append(applied)
        return applied

    def patch_grant_status(self, namespace, name, status):
        self.grant_statuses.append((namespace, name, status))

    def patch_demand_status(self, namespace, name, status):
        self.demand_statuses.append((namespace, name, status))


def demand():
    return {
        "metadata": {
            "name": "topology",
            "namespace": "demo",
            "uid": "ngd-uid",
            "generation": 1,
        },
        "status": {},
    }


def grant():
    groups = []
    for rank, group_id in enumerate(("switch-c", "switch-a", "switch-b"), start=1):
        groups.append(
            {
                "rank": rank,
                "groupId": group_id,
                "nodes": [
                    {"name": f"{group_id}-node", "uid": f"uid-{group_id}", "score": 90}
                ],
            }
        )
    return {
        "metadata": {
            "name": "ngg-topology",
            "namespace": "demo",
            "uid": "ngg-uid",
            "generation": 1,
        },
        "spec": {
            "revision": 1,
            "activeGroupRef": {"rank": 1, "groupId": "switch-c"},
            "candidateNodeGroups": groups,
            "groupAttemptPolicy": {"timeoutSeconds": 15},
        },
        "status": {
            "phase": "Active",
            "activeGroupState": "Trying",
            "attemptStartedAt": "2026-08-14T10:00:00Z",
        },
    }


class ControllerStateTests(unittest.TestCase):
    def setUp(self):
        self.kube = FakeKube()
        self.controller = PRCController(self.kube, None, "demo")

    def test_zero_bind_timeout_advances_exactly_one_rank(self):
        handled = self.controller._advance_group(
            demand(), grant(), [], "task-uid", datetime.now(timezone.utc)
        )
        self.assertTrue(handled)
        applied = self.kube.updated_grants[-1]
        self.assertEqual(applied["spec"]["activeGroupRef"], {"rank": 2, "groupId": "switch-a"})
        self.assertEqual(applied["spec"]["revision"], 2)
        status = self.kube.grant_statuses[-1][2]
        self.assertEqual(status["activeGroupState"], "Trying")
        self.assertEqual(status["observedRevision"], 2)

    def test_bound_pod_locks_group_without_switching(self):
        pod = {
            "metadata": {
                "namespace": "demo",
                "ownerReferences": [{"uid": "task-uid"}],
            },
            "spec": {"nodeName": "switch-c-node"},
            "status": {"phase": "Running"},
        }
        handled = self.controller._handle_existing_grant(
            demand(), grant(), [pod], "task-uid"
        )
        self.assertTrue(handled)
        self.assertEqual(self.kube.updated_grants, [])
        self.assertEqual(
            self.kube.grant_statuses[-1][2]["activeGroupState"], "Locked"
        )


if __name__ == "__main__":
    unittest.main()
