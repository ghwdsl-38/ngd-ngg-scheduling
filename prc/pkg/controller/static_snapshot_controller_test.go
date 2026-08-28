package controller

import (
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

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
}
