package nodegroupgrant

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"volcano.sh/volcano/pkg/scheduler/api"
)

func testGrant() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "scheduling.demo.ngg.io/v1alpha1",
		"kind": "NodeGroupGrant",
		"metadata": map[string]interface{}{
			"name": "ngg-demo", "namespace": "demo", "generation": int64(2),
		},
		"spec": map[string]interface{}{
			"demandRef": map[string]interface{}{"name": "demo", "uid": "demand-uid"},
			"taskRef": map[string]interface{}{"uid": "task-uid"},
			"schedulerName": "volcano",
			"revision": int64(3),
			"validUntil": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"activeGroupRef": map[string]interface{}{"rank": int64(1), "groupId": "switch-a"},
			"candidateNodeGroups": []interface{}{
				map[string]interface{}{
					"rank": int64(1), "groupId": "switch-a",
					"nodes": []interface{}{map[string]interface{}{"name": "node-a", "uid": "node-uid"}},
				},
				map[string]interface{}{
					"rank": int64(2), "groupId": "switch-b",
					"nodes": []interface{}{map[string]interface{}{"name": "node-b", "uid": "node-b-uid"}},
				},
			},
		},
		"status": map[string]interface{}{
			"phase": "Active", "activeGroupState": "Trying",
			"observedGeneration": int64(2), "observedRevision": int64(3),
		},
	}}
}

func TestOnlyActiveGroupIsAuthorized(t *testing.T) {
	u := testGrant()
	g, err := parseGrant(u)
	if err != nil {
		t.Fatalf("parseGrant returned error: %v", err)
	}
	if _, found := g.nodes["node-b"]; found {
		t.Fatal("inactive candidate group must not be merged into the authorized node set")
	}
	_ = unstructured.SetNestedField(u.Object, int64(2), "spec", "activeGroupRef", "rank")
	_ = unstructured.SetNestedField(u.Object, "switch-b", "spec", "activeGroupRef", "groupId")
	g, err = parseGrant(u)
	if err != nil {
		t.Fatalf("parse switched grant: %v", err)
	}
	if g.nodes["node-b"] != types.UID("node-b-uid") {
		t.Fatalf("second candidate group was not activated: %#v", g.nodes)
	}
}

func TestParseGrantAndValidity(t *testing.T) {
	g, err := parseGrant(testGrant())
	if err != nil {
		t.Fatalf("parseGrant returned error: %v", err)
	}
	if !g.valid(time.Now()) {
		t.Fatal("expected grant to be valid")
	}
	if g.nodes["node-a"] != types.UID("node-uid") {
		t.Fatalf("unexpected node map: %#v", g.nodes)
	}
}

func TestGenerationMismatchIsInvalid(t *testing.T) {
	u := testGrant()
	_ = unstructured.SetNestedField(u.Object, int64(1), "status", "observedGeneration")
	g, err := parseGrant(u)
	if err != nil {
		t.Fatalf("parseGrant returned error: %v", err)
	}
	if g.valid(time.Now()) {
		t.Fatal("grant with stale observedGeneration must be invalid")
	}
}

func TestEvaluateIsOptInAndEnforcesNodeBoundary(t *testing.T) {
	node := &api.NodeInfo{Name: "node-a", Node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "node-uid"}}}
	unmanaged := &api.TaskInfo{Namespace: "demo", Pod: &corev1.Pod{}}
	if err := evaluate(storeSnapshot{}, unmanaged, node); err != nil {
		t.Fatalf("unmanaged Pod should skip plugin: %v", err)
	}

	g, _ := parseGrant(testGrant())
	snapshot := storeSnapshot{ready: true, byDemand: map[string]grant{"demo/demo": g}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "demo",
			Annotations: map[string]string{DemandAnnotation: "demo"},
			OwnerReferences: []metav1.OwnerReference{{UID: "task-uid"}},
		},
		Spec: corev1.PodSpec{SchedulerName: "volcano"},
	}
	managed := &api.TaskInfo{Namespace: "demo", Pod: pod}
	if err := evaluate(snapshot, managed, node); err != nil {
		t.Fatalf("allowed Node was rejected: %v", err)
	}
	other := &api.NodeInfo{Name: "node-b", Node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-b", UID: "node-b-uid"}}}
	if err := evaluate(snapshot, managed, other); err == nil {
		t.Fatal("Node outside NGG must be rejected")
	}
}
