from __future__ import annotations

import copy
import logging
import time
from datetime import datetime, timedelta, timezone
from typing import Any

from .algorithm_client import AlgorithmClient
from .kube import ApiError, GROUP, VERSION, KubeClient
from .snapshots import build_scheduler_state, build_static_snapshot


LOG = logging.getLogger(__name__)


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


def timestamp(value: datetime) -> str:
    return value.isoformat(timespec="seconds").replace("+00:00", "Z")


def parse_timestamp(value: str) -> datetime | None:
    if not value:
        return None
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def grant_name(demand_name: str) -> str:
    return f"ngg-{demand_name}"[:253].rstrip("-")


def task_pods(pods: list[dict[str, Any]], namespace: str, task_uid: str) -> list[dict[str, Any]]:
    result = []
    for pod in pods:
        metadata = pod.get("metadata", {})
        if metadata.get("namespace") != namespace:
            continue
        if any(str(owner.get("uid", "")) == task_uid for owner in metadata.get("ownerReferences", [])):
            result.append(pod)
    return result


class PRCController:
    def __init__(
        self,
        client: KubeClient,
        algorithm: AlgorithmClient,
        cluster_id: str,
    ) -> None:
        self.client = client
        self.algorithm = algorithm
        self.cluster_id = cluster_id

    def run(self, interval: int) -> None:
        while True:
            try:
                self.reconcile()
            except Exception:
                LOG.exception("PRC reconciliation failed")
            time.sleep(interval)

    def reconcile(self) -> None:
        nodes = self.client.list_nodes()
        pods = self.client.list_pods()
        topologies = self.client.list_topologies()
        static_id, static_snapshot = build_static_snapshot(
            self.cluster_id, nodes, topologies
        )
        if not static_snapshot["nodes"]:
            raise RuntimeError("no Worker has a usable NodeNetworkTopology status")
        static_ack = self.algorithm.sync_static_snapshot(static_id, static_snapshot)
        if static_ack.get("acceptedSnapshotId") != static_id:
            raise RuntimeError("Algorithm did not acknowledge the static snapshot")
        state_id, state_captured_at, scheduler_state = build_scheduler_state(nodes, pods)

        grants = {
            (item["metadata"]["namespace"], item["metadata"]["name"]): item
            for item in self.client.list_grants()
        }
        for demand in self.client.list_demands():
            try:
                self._reconcile_demand(
                    demand,
                    grants.get(
                        (
                            demand["metadata"]["namespace"],
                            grant_name(demand["metadata"]["name"]),
                        )
                    ),
                    pods,
                    static_id,
                    state_id,
                    state_captured_at,
                    scheduler_state,
                    static_ack,
                )
            except Exception as exc:
                LOG.exception(
                    "failed to reconcile demand %s/%s",
                    demand["metadata"].get("namespace"),
                    demand["metadata"].get("name"),
                )
                self._patch_demand_error(demand, str(exc))

    def _reconcile_demand(
        self,
        demand: dict[str, Any],
        existing: dict[str, Any] | None,
        pods: list[dict[str, Any]],
        static_id: str,
        state_id: str,
        state_captured_at: str,
        scheduler_state: list[dict[str, Any]],
        static_ack: dict[str, Any],
    ) -> None:
        metadata = demand["metadata"]
        namespace = metadata["namespace"]
        name = metadata["name"]
        spec = demand["spec"]
        try:
            task = self.client.get_task(namespace, spec["taskRef"])
        except ApiError as exc:
            if exc.status != 404:
                raise
            self.client.patch_demand_status(
                namespace,
                name,
                self._demand_status(
                    demand,
                    "Pending",
                    "TaskNotFound",
                    "waiting for referenced workload",
                ),
            )
            return

        task_uid = str(task["metadata"]["uid"])
        requested_uid = str(spec["taskRef"].get("uid", ""))
        if requested_uid and requested_uid != task_uid:
            raise ValueError("taskRef.uid does not match the live workload UID")

        demand_generation = int(metadata.get("generation", 0))
        if existing and self._grant_matches_demand(existing, metadata, task_uid):
            if self._handle_existing_grant(demand, existing, pods, task_uid):
                return

        request_id = (
            f"{metadata['uid']}-generation-{demand_generation}-"
            f"state-{state_id.removeprefix('sha256:')[:8]}"
        )
        grant_policy = self._grant_policy(spec)
        response = self.algorithm.calculate(
            {
                "requestId": request_id,
                "taskUID": task_uid,
                "ngdUID": str(metadata["uid"]),
                "ngdGeneration": demand_generation,
                "nodeStaticSnapshotId": static_id,
                "schedulerStateSnapshotId": state_id,
                "schedulerStateCapturedAt": state_captured_at,
                "podSets": spec["podSets"],
                "nodeRequirements": spec.get("nodeRequirements", {}),
                "topologyRequirement": spec.get(
                    "topologyRequirement", {"level": "leafGroup", "mode": "same"}
                ),
                "algorithms": spec.get("algorithms", []),
                "maxCandidateGroups": grant_policy["maxCandidateGroups"],
                "schedulerState": scheduler_state,
            }
        )
        self._validate_response(
            response,
            request_id,
            metadata,
            task_uid,
            static_id,
            state_id,
            str(static_ack.get("algorithmBootId", "")),
            {str(item.get("nodeUID", "")) for item in scheduler_state},
        )
        groups = self._grant_groups(response.get("candidateNodeGroups", []))
        now = utcnow()
        ttl = grant_policy["ttlSeconds"]
        revision = int((existing or {}).get("spec", {}).get("revision", 0)) + 1
        desired_spec: dict[str, Any] = {
            "demandRef": {
                "name": name,
                "uid": str(metadata["uid"]),
                "generation": demand_generation,
            },
            "taskRef": {
                "apiVersion": spec["taskRef"]["apiVersion"],
                "kind": spec["taskRef"]["kind"],
                "name": spec["taskRef"]["name"],
                "uid": task_uid,
            },
            "schedulerName": spec["schedulerName"],
            "source": "prc-algorithm",
            "candidateNodeGroups": groups,
            "groupAttemptPolicy": {
                "timeoutSeconds": grant_policy["groupAttemptTimeoutSeconds"],
                "lockAfterFirstBind": True,
            },
            "dataVersions": {
                "algorithmBootId": str(response.get("algorithmBootId", "")),
                "nodeStaticSnapshotId": static_id,
                "schedulerStateSnapshotId": state_id,
                "metricSnapshotId": str(response.get("metricSnapshotId", "")),
            },
            "revision": revision,
            "generatedAt": timestamp(now),
            "validUntil": timestamp(now + timedelta(seconds=ttl)),
        }
        if groups:
            desired_spec["activeGroupRef"] = {
                "rank": 1,
                "groupId": groups[0]["groupId"],
            }
        applied = self._upsert_grant(demand, existing, desired_spec)

        if groups:
            phase, active_state = "Active", "Trying"
            reason, message = "ActiveGroupReady", f"candidateGroups={len(groups)}"
        else:
            phase, active_state = "Inactive", "Exhausted"
            reason, message = "NoFeasibleGroup", "candidateGroups=0"
        grant_status = self._grant_status(
            applied,
            phase,
            active_state,
            task_pods(pods, namespace, task_uid),
            attempt_started_at="",
            reason=reason,
            message=message,
        )
        self.client.patch_grant_status(
            namespace, applied["metadata"]["name"], grant_status
        )
        self._patch_fulfillment(
            demand,
            applied,
            task_uid,
            fulfilled=bool(groups),
            reason=reason,
            message=message,
        )

    def _handle_existing_grant(
        self,
        demand: dict[str, Any],
        grant: dict[str, Any],
        pods: list[dict[str, Any]],
        task_uid: str,
    ) -> bool:
        metadata = demand["metadata"]
        namespace = metadata["namespace"]
        status = grant.get("status", {})
        phase = str(status.get("phase", ""))
        active_state = str(status.get("activeGroupState", "Trying"))
        task_pod_items = task_pods(pods, namespace, task_uid)
        bound_count = sum(
            1 for pod in task_pod_items if pod.get("spec", {}).get("nodeName")
        )
        now = utcnow()

        if phase == "Inactive" and active_state == "Exhausted":
            valid_until = parse_timestamp(str(grant.get("spec", {}).get("validUntil", "")))
            if valid_until and now < valid_until:
                self._patch_fulfillment(
                    demand,
                    grant,
                    task_uid,
                    False,
                    "CandidateGroupsExhausted",
                    "waiting for grant TTL or a new demand generation",
                )
                return True
            return False

        if phase != "Active":
            return False

        if active_state == "Locked" or bound_count > 0:
            new_status = self._grant_status(
                grant,
                "Active",
                "Locked",
                task_pod_items,
                attempt_started_at=str(status.get("attemptStartedAt", "")),
                reason="ActiveGroupLocked",
                message=f"boundPodCount={bound_count}",
            )
            if status != new_status:
                self.client.patch_grant_status(
                    namespace, grant["metadata"]["name"], new_status
                )
            self._patch_fulfillment(
                demand,
                grant,
                task_uid,
                True,
                "ActiveGroupLocked",
                f"boundPodCount={bound_count}",
            )
            return True

        nonterminal = [
            pod
            for pod in task_pod_items
            if pod.get("status", {}).get("phase") not in ("Succeeded", "Failed")
        ]
        attempt_started = parse_timestamp(str(status.get("attemptStartedAt", "")))
        if not nonterminal:
            attempt_started = None
        elif attempt_started is None:
            attempt_started = now

        timeout_seconds = int(
            grant.get("spec", {})
            .get("groupAttemptPolicy", {})
            .get("timeoutSeconds", 30)
        )
        if attempt_started and now - attempt_started >= timedelta(seconds=timeout_seconds):
            return self._advance_group(demand, grant, task_pod_items, task_uid, now)

        new_status = self._grant_status(
            grant,
            "Active",
            "Trying",
            task_pod_items,
            attempt_started_at=timestamp(attempt_started) if attempt_started else "",
            reason="ActiveGroupTrying",
            message=(
                f"waitingForBind timeoutSeconds={timeout_seconds}"
                if attempt_started
                else "waitingForTaskPods"
            ),
        )
        if status != new_status:
            self.client.patch_grant_status(
                namespace, grant["metadata"]["name"], new_status
            )
        self._patch_fulfillment(
            demand,
            grant,
            task_uid,
            True,
            "ActiveGroupTrying",
            new_status["conditions"][0]["message"],
        )
        return True

    def _advance_group(
        self,
        demand: dict[str, Any],
        grant: dict[str, Any],
        task_pod_items: list[dict[str, Any]],
        task_uid: str,
        now: datetime,
    ) -> bool:
        namespace = grant["metadata"]["namespace"]
        # Re-checking this list immediately before the update is the demo's
        # zero-bind guard. The API implementation uses one polling snapshot.
        if any(pod.get("spec", {}).get("nodeName") for pod in task_pod_items):
            return False
        groups = grant["spec"].get("candidateNodeGroups", [])
        active_rank = int(grant["spec"].get("activeGroupRef", {}).get("rank", 1))
        if active_rank < len(groups):
            updated = copy.deepcopy(grant)
            next_group = groups[active_rank]
            updated["spec"]["activeGroupRef"] = {
                "rank": active_rank + 1,
                "groupId": next_group["groupId"],
            }
            updated["spec"]["revision"] = int(grant["spec"]["revision"]) + 1
            updated["spec"]["generatedAt"] = timestamp(now)
            applied = self.client.update_grant(updated)
            new_status = self._grant_status(
                applied,
                "Active",
                "Trying",
                task_pod_items,
                attempt_started_at=timestamp(now),
                reason="ActiveGroupAdvanced",
                message=f"advancedToRank={active_rank + 1}",
            )
            self.client.patch_grant_status(
                namespace, applied["metadata"]["name"], new_status
            )
            self._patch_fulfillment(
                demand,
                applied,
                task_uid,
                True,
                "ActiveGroupAdvanced",
                f"advancedToRank={active_rank + 1}",
            )
            LOG.info(
                "grant=%s/%s advanced rank %d -> %d",
                namespace,
                applied["metadata"]["name"],
                active_rank,
                active_rank + 1,
            )
            return True

        exhausted = self._grant_status(
            grant,
            "Inactive",
            "Exhausted",
            task_pod_items,
            attempt_started_at=str(grant.get("status", {}).get("attemptStartedAt", "")),
            reason="CandidateGroupsExhausted",
            message=f"attemptedGroups={len(groups)}",
        )
        self.client.patch_grant_status(namespace, grant["metadata"]["name"], exhausted)
        self._patch_fulfillment(
            demand,
            grant,
            task_uid,
            False,
            "CandidateGroupsExhausted",
            f"attemptedGroups={len(groups)}",
        )
        return True

    @staticmethod
    def _grant_matches_demand(
        grant: dict[str, Any], demand_metadata: dict[str, Any], task_uid: str
    ) -> bool:
        spec = grant.get("spec", {})
        demand_ref = spec.get("demandRef", {})
        return (
            str(demand_ref.get("uid", "")) == str(demand_metadata.get("uid", ""))
            and int(demand_ref.get("generation", 0))
            == int(demand_metadata.get("generation", 0))
            and str(spec.get("taskRef", {}).get("uid", "")) == task_uid
        )

    @staticmethod
    def _grant_policy(spec: dict[str, Any]) -> dict[str, int]:
        policy = spec.get("grantPolicy", {})
        legacy = spec.get("candidatePolicy", {})
        return {
            "ttlSeconds": int(policy.get("ttlSeconds", legacy.get("grantTTLSeconds", 600))),
            "maxCandidateGroups": min(3, max(1, int(policy.get("maxCandidateGroups", 3)))),
            "groupAttemptTimeoutSeconds": int(
                policy.get("groupAttemptTimeoutSeconds", 30)
            ),
        }

    @staticmethod
    def _validate_response(
        response: dict[str, Any],
        request_id: str,
        demand_metadata: dict[str, Any],
        task_uid: str,
        static_id: str,
        state_id: str,
        algorithm_boot_id: str,
        known_node_uids: set[str],
    ) -> None:
        if response.get("requestId") != request_id:
            raise ValueError("Algorithm response requestId mismatch")
        if response.get("taskUID") != task_uid:
            raise ValueError("Algorithm response taskUID mismatch")
        if response.get("ngdUID") != str(demand_metadata["uid"]):
            raise ValueError("Algorithm response ngdUID mismatch")
        if int(response.get("ngdGeneration", 0)) != int(
            demand_metadata.get("generation", 0)
        ):
            raise ValueError("Algorithm response ngdGeneration mismatch")
        if response.get("nodeStaticSnapshotId") != static_id:
            raise ValueError("Algorithm response nodeStaticSnapshotId mismatch")
        if response.get("schedulerStateSnapshotId") != state_id:
            raise ValueError("Algorithm response schedulerStateSnapshotId mismatch")
        if response.get("algorithmBootId") != algorithm_boot_id:
            raise ValueError("Algorithm response bootId mismatch")
        groups = response.get("candidateNodeGroups", [])
        if len(groups) > 3:
            raise ValueError("Algorithm returned more than 3 candidate groups")
        if response.get("status") == "SUCCESS" and not groups:
            raise ValueError("SUCCESS response contains no candidate group")
        if response.get("status") not in ("SUCCESS", "UNSATISFIABLE"):
            raise ValueError(f"unsupported Algorithm status {response.get('status')!r}")
        previous_score: float | None = None
        previous_id = ""
        seen_nodes: set[str] = set()
        for rank, group in enumerate(groups, start=1):
            if int(group.get("rank", 0)) != rank:
                raise ValueError("candidate group rank is not continuous")
            score = float(group.get("groupScore", 0))
            group_id = str(group.get("groupId", ""))
            if previous_score is not None and (
                score > previous_score
                or (score == previous_score and group_id < previous_id)
            ):
                raise ValueError("candidate groups are not stably sorted")
            previous_score, previous_id = score, group_id
            for node in group.get("nodes", []):
                uid = str(node.get("nodeUID", ""))
                if not uid:
                    raise ValueError("candidate node has no UID")
                if uid not in known_node_uids:
                    raise ValueError(f"Algorithm returned unknown node UID {uid}")
                if uid in seen_nodes:
                    raise ValueError("candidate groups must be mutually exclusive")
                seen_nodes.add(uid)

    @staticmethod
    def _grant_groups(groups: list[dict[str, Any]]) -> list[dict[str, Any]]:
        result = []
        for group in groups:
            result.append(
                {
                    "rank": int(group["rank"]),
                    "groupId": str(group["groupId"]),
                    "topologyLevel": str(group.get("topologyLevel", "leafGroup")),
                    "groupScore": float(group["groupScore"]),
                    "nodes": [
                        {
                            "name": str(node["nodeName"]),
                            "uid": str(node["nodeUID"]),
                            "score": int(node["score"]),
                        }
                        for node in group["nodes"]
                    ],
                }
            )
        return result

    def _upsert_grant(
        self,
        demand: dict[str, Any],
        existing: dict[str, Any] | None,
        desired_spec: dict[str, Any],
    ) -> dict[str, Any]:
        metadata = demand["metadata"]
        if existing is None:
            body = {
                "apiVersion": f"{GROUP}/{VERSION}",
                "kind": "NodeGroupGrant",
                "metadata": {
                    "name": grant_name(metadata["name"]),
                    "namespace": metadata["namespace"],
                    "labels": {
                        "scheduling.demo.ngg.io/demand": metadata["name"],
                        "scheduling.demo.ngg.io/scheduler": demand["spec"]["schedulerName"],
                    },
                    "ownerReferences": [
                        {
                            "apiVersion": f"{GROUP}/{VERSION}",
                            "kind": "NodeGroupDemand",
                            "name": metadata["name"],
                            "uid": metadata["uid"],
                            "controller": True,
                        }
                    ],
                },
                "spec": desired_spec,
            }
            return self.client.create_grant(metadata["namespace"], body)
        updated = copy.deepcopy(existing)
        updated["spec"] = desired_spec
        return self.client.update_grant(updated)

    @staticmethod
    def _grant_status(
        grant: dict[str, Any],
        phase: str,
        active_state: str,
        pod_items: list[dict[str, Any]],
        attempt_started_at: str,
        reason: str,
        message: str,
    ) -> dict[str, Any]:
        active = grant.get("spec", {}).get("activeGroupRef", {})
        groups = grant.get("spec", {}).get("candidateNodeGroups", [])
        active_nodes = next(
            (
                group.get("nodes", [])
                for group in groups
                if group.get("groupId") == active.get("groupId")
                and int(group.get("rank", 0)) == int(active.get("rank", 0))
            ),
            [],
        )
        ready = phase == "Active" and bool(active_nodes)
        return {
            "phase": phase,
            "observedGeneration": int(grant["metadata"].get("generation", 0)),
            "observedRevision": int(grant.get("spec", {}).get("revision", 0)),
            "activeGroupState": active_state,
            "activeGroupId": str(active.get("groupId", "")),
            "activeGroupRank": int(active.get("rank", 0)),
            "attemptStartedAt": attempt_started_at,
            "boundPodCount": sum(
                1 for pod in pod_items if pod.get("spec", {}).get("nodeName")
            ),
            "activeNodeCount": len(active_nodes),
            "conditions": [
                {
                    "type": "Ready",
                    "status": "True" if ready else "False",
                    "reason": reason,
                    "message": message,
                }
            ],
        }

    def _patch_fulfillment(
        self,
        demand: dict[str, Any],
        grant: dict[str, Any],
        task_uid: str,
        fulfilled: bool,
        reason: str,
        message: str,
    ) -> None:
        status = {
            "phase": "Fulfilled" if fulfilled else "Unsatisfied",
            "observedGeneration": int(demand["metadata"].get("generation", 0)),
            "resolvedTaskUID": task_uid,
            "grantRef": {
                "name": grant["metadata"]["name"],
                "uid": str(grant["metadata"]["uid"]),
            },
            "conditions": [
                {
                    "type": "Fulfilled",
                    "status": "True" if fulfilled else "False",
                    "reason": reason,
                    "message": message,
                }
            ],
        }
        if demand.get("status", {}) != status:
            self.client.patch_demand_status(
                demand["metadata"]["namespace"], demand["metadata"]["name"], status
            )

    @staticmethod
    def _demand_status(
        demand: dict[str, Any], phase: str, reason: str, message: str
    ) -> dict[str, Any]:
        return {
            "phase": phase,
            "observedGeneration": int(demand["metadata"].get("generation", 0)),
            "conditions": [
                {
                    "type": "Fulfilled",
                    "status": "False",
                    "reason": reason,
                    "message": message,
                }
            ],
        }

    def _patch_demand_error(self, demand: dict[str, Any], message: str) -> None:
        try:
            self.client.patch_demand_status(
                demand["metadata"]["namespace"],
                demand["metadata"]["name"],
                self._demand_status(
                    demand, "Degraded", "ReconcileError", message[:512]
                ),
            )
        except Exception:
            LOG.exception("failed to patch error status")
