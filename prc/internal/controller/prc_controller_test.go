package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestValidateResponsePreservesStableAlgorithmOrder(t *testing.T) {
	demand := newUnstructured(demandGVK)
	demand.SetUID(types.UID("ngd-uid"))
	demand.SetGeneration(2)
	state := []schedulerNodeState{{NodeUID: "node-a"}, {NodeUID: "node-b"}}
	response := AlgorithmResponse{
		RequestID: "request-1", TaskUID: "task-uid", NGDUID: "ngd-uid", NGDGeneration: 2,
		AlgorithmBootID: "boot-1", NodeStaticSnapshotID: "static-1", SchedulerStateSnapshotID: "state-1",
		Status: "SUCCESS",
		CandidateNodeGroups: []CandidateGroup{
			{Rank: 1, GroupID: "switch-b", GroupScore: 90, Nodes: []CandidateNode{{NodeUID: "node-b"}}},
			{Rank: 2, GroupID: "switch-a", GroupScore: 80, Nodes: []CandidateNode{{NodeUID: "node-a"}}},
		},
	}
	if err := validateResponse(response, "request-1", demand, types.UID("task-uid"), "static-1", "state-1", "boot-1", state); err != nil {
		t.Fatalf("valid Algorithm order was rejected: %v", err)
	}

	response.CandidateNodeGroups[0], response.CandidateNodeGroups[1] = response.CandidateNodeGroups[1], response.CandidateNodeGroups[0]
	response.CandidateNodeGroups[0].Rank = 1
	response.CandidateNodeGroups[1].Rank = 2
	if err := validateResponse(response, "request-1", demand, types.UID("task-uid"), "static-1", "state-1", "boot-1", state); err == nil {
		t.Fatal("lower-scored group before higher-scored group was accepted")
	}
}

func TestActiveGroupInvalidUsesNodeNameUIDAndAvailability(t *testing.T) {
	grant := newUnstructured(grantGVK)
	grant.Object["spec"] = map[string]any{
		"activeGroupRef": map[string]any{"rank": int64(1), "groupId": "switch-a"},
		"candidateNodeGroups": []any{map[string]any{
			"rank": int64(1), "groupId": "switch-a",
			"nodes": []any{map[string]any{"name": "worker-a", "uid": "uid-a"}},
		}},
	}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-a", UID: types.UID("uid-a")}}
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	if activeGroupInvalid(grant, []corev1.Node{node}) {
		t.Fatal("healthy active Node was treated as invalid")
	}
	node.Spec.Unschedulable = true
	if !activeGroupInvalid(grant, []corev1.Node{node}) {
		t.Fatal("cordoned active Node was not treated as invalid")
	}
}

func TestDirectTaskPodsRequiresDirectOwnerUID(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "direct", Namespace: "demo", OwnerReferences: []metav1.OwnerReference{{UID: types.UID("task-1")}}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "demo", OwnerReferences: []metav1.OwnerReference{{UID: types.UID("other")}}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "wrong-namespace", Namespace: "other", OwnerReferences: []metav1.OwnerReference{{UID: types.UID("task-1")}}}},
	}
	got := directTaskPods(pods, "demo", types.UID("task-1"))
	if len(got) != 1 || got[0].Name != "direct" {
		t.Fatalf("expected only the directly owned Pod, got %#v", got)
	}
}

func TestStaticSnapshotHashIsContentBasedAndStable(t *testing.T) {
	valueA := map[string]any{"nodes": []any{"b", "a"}, "clusterId": "demo"}
	valueB := map[string]any{"clusterId": "demo", "nodes": []any{"b", "a"}}
	hashA, err := contentHash(valueA)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := contentHash(valueB)
	if err != nil {
		t.Fatal(err)
	}
	if hashA != hashB || len(hashA) != 71 || hashA[:7] != "sha256:" {
		t.Fatalf("unexpected content hashes %q and %q", hashA, hashB)
	}
}

func TestExhaustedGrantWaitsForSnapshotContentChange(t *testing.T) {
	reconciler := &NodeGroupDemandReconciler{}
	demand := newUnstructured(demandGVK)
	grant := newUnstructured(grantGVK)
	grant.Object["spec"] = map[string]any{"dataVersions": map[string]any{
		"nodeStaticSnapshotId": "static-1", "schedulerStateSnapshotId": "state-1",
	}}
	grant.Object["status"] = map[string]any{"phase": "Inactive", "activeGroupState": "Exhausted"}
	handled, _, err := reconciler.handleExisting(
		context.Background(), demand, grant, nil, nil, "static-1", "state-1", policyValues{},
	)
	if err != nil || !handled {
		t.Fatalf("unchanged exhausted grant should remain quiescent: handled=%v err=%v", handled, err)
	}
	handled, _, err = reconciler.handleExisting(
		context.Background(), demand, grant, nil, nil, "static-2", "state-1", policyValues{},
	)
	if err != nil || handled {
		t.Fatalf("changed static snapshot should trigger recomputation: handled=%v err=%v", handled, err)
	}
}

func TestNestedNumberAcceptsKubernetesIntegerAndFloatRepresentations(t *testing.T) {
	object := map[string]any{"status": map[string]any{
		"bandwidthGbps": int64(25), "latencyMillis": 1.5,
	}}
	if got := nestedNumber(object, "status", "bandwidthGbps"); got != 25 {
		t.Fatalf("integer bandwidth decoded as %v", got)
	}
	if got := nestedNumber(object, "status", "latencyMillis"); got != 1.5 {
		t.Fatalf("float latency decoded as %v", got)
	}
}

func TestStaticSnapshotPrefersThreeLevelNodeLabels(t *testing.T) {
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "worker-a", UID: types.UID("uid-a"), Labels: map[string]string{
			"topology.demo.ngg.io/leaf-switch":      "leaf-a",
			"topology.demo.ngg.io/border-switch":    "border-a",
			"topology.demo.ngg.io/core-switch":      "core-0",
			"topology.demo.ngg.io/bandwidth-gbps":   "25",
			"topology.demo.ngg.io/latency-ms":       "1.5",
			"topology.demo.ngg.io/topology-version": "leaf-border-core-v1",
		},
	}}
	node.Status.Allocatable = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("64")}
	_, snapshot, err := buildStaticSnapshot("demo", []corev1.Node{node}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Nodes) != 1 {
		t.Fatalf("got %d Nodes", len(snapshot.Nodes))
	}
	topology := snapshot.Nodes[0].Topology
	if topology.LeafSwitchID != "leaf-a" || topology.BorderSwitchID != "border-a" || topology.CoreSwitchID != "core-0" {
		t.Fatalf("unexpected three-level topology: %#v", topology)
	}
	if snapshot.TopologyVersion != "leaf-border-core-v1" {
		t.Fatalf("unexpected version %q", snapshot.TopologyVersion)
	}
}
