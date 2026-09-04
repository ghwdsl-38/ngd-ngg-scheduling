package controller

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestBuildStaticSnapshotUsesNodeLeafAnnotationsWithoutNNTFallback(t *testing.T) {
	leaves, _ := json.Marshal([]string{"leaf-b", "leaf-a"})
	links := `[{"bond":"bond0","bondMode":"802.3ad","interface":"eth1","leafSwitchId":"leaf-b","active":true},{"bond":"bond0","bondMode":"802.3ad","interface":"eth0","leafSwitchId":"leaf-a","active":true}]`
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "worker-dual", UID: "uid-dual",
		Annotations: map[string]string{
			"topology.demo.ngg.io/leaf-switch-ids": string(leaves),
			"topology.demo.ngg.io/leaf-links":      links,
		},
	}}
	_, raw, err := BuildStaticSnapshot("cluster-1", []corev1.Node{node})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := raw.(staticSnapshot)
	if len(snapshot.Nodes) != 1 || len(snapshot.Nodes[0].Topology.LeafSwitchIDs) != 2 || snapshot.Nodes[0].Topology.LeafSwitchIDs[0] != "leaf-a" {
		t.Fatalf("unexpected dual-Leaf snapshot: %#v", snapshot)
	}

	node.Annotations = nil
	if _, _, err := BuildStaticSnapshot("cluster-1", []corev1.Node{node}); err == nil {
		t.Fatal("Node without Leaf metadata must not fall back to NodeNetworkTopology")
	}
}

func TestStaticSnapshotStateFailsClosedWhileNewSnapshotSyncs(t *testing.T) {
	state := NewStaticSnapshotState()
	now := time.Now().UTC()
	state.confirmed("sha256:old", "boot-1", 3, now)
	if status, ready := state.Current(); !ready || status.SnapshotID != "sha256:old" {
		t.Fatalf("confirmed state = %#v ready=%v", status, ready)
	}

	state.syncing(now.Add(time.Second))
	if _, ready := state.Current(); ready {
		t.Fatal("state must fail closed while a changed static snapshot is not acknowledged")
	}
	state.failed(errors.New("Algorithm unavailable"), now.Add(2*time.Second))
	if status, ready := state.Current(); ready || status.LastError == "" {
		t.Fatalf("failed state = %#v ready=%v", status, ready)
	}

	state.confirmed("sha256:new", "boot-2", 4, now.Add(3*time.Second))
	if status, ready := state.Current(); !ready || status.SnapshotID != "sha256:new" || status.AlgorithmBootID != "boot-2" {
		t.Fatalf("resynchronized state = %#v ready=%v", status, ready)
	}
}

func TestStaticNodeChangePredicateIgnoresHeartbeatOnlyUpdate(t *testing.T) {
	predicate := staticNodeChangePredicate()
	base := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-1", Labels: map[string]string{"topology.demo.ngg.io/leaf-switch": "leaf-a"}},
		Status:     corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")}},
	}

	heartbeat := base.DeepCopy()
	heartbeat.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastHeartbeatTime: metav1.Now()}}
	if predicate.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: heartbeat}) {
		t.Fatal("heartbeat-only update must not rebuild the cluster static snapshot")
	}

	labelChanged := base.DeepCopy()
	labelChanged.Labels["topology.demo.ngg.io/leaf-switch"] = "leaf-b"
	if !predicate.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: labelChanged}) {
		t.Fatal("static label update must trigger snapshot synchronization")
	}

	allocatableChanged := base.DeepCopy()
	allocatableChanged.Status.Allocatable[corev1.ResourceCPU] = resource.MustParse("16")
	if !predicate.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: allocatableChanged}) {
		t.Fatal("Allocatable update must trigger snapshot synchronization")
	}

	leafLinksChanged := base.DeepCopy()
	leafLinksChanged.Annotations = map[string]string{"topology.demo.ngg.io/leaf-switch-ids": `["leaf-a","leaf-b"]`}
	if !predicate.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: leafLinksChanged}) {
		t.Fatal("Leaf connection annotation update must trigger snapshot synchronization")
	}

	observedAtOnly := base.DeepCopy()
	observedAtOnly.Annotations = map[string]string{"topology.demo.ngg.io/observed-at": time.Now().UTC().Format(time.RFC3339)}
	if predicate.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: observedAtOnly}) {
		t.Fatal("diagnostic observed-at update must not rebuild the static snapshot")
	}
}
