package nodegroupgrant

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func testGrant() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "scheduling.demo.ngg.io/v1alpha1",
		"kind":       "NodeGroupGrant",
		"metadata": map[string]interface{}{
			"name": "ngg-demo", "namespace": "demo", "generation": int64(2),
		},
		"spec": map[string]interface{}{
			"demandRef":     map[string]interface{}{"name": "demo", "uid": "demand-uid"},
			"taskRef":       map[string]interface{}{"uid": "task-uid"},
			"schedulerName": schedulerName,
			"revision":      int64(3),
			"validUntil":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"activeGroupRef": map[string]interface{}{
				"rank": int64(2), "groupId": "switch-a",
			},
			"candidateNodeGroups": []interface{}{
				map[string]interface{}{
					"rank": int64(1), "groupId": "switch-c",
					"nodes": []interface{}{map[string]interface{}{"name": "node-c", "uid": "uid-c"}},
				},
				map[string]interface{}{
					"rank": int64(2), "groupId": "switch-a",
					"nodes": []interface{}{map[string]interface{}{"name": "node-a", "uid": "uid-a"}},
				},
			},
		},
		"status": map[string]interface{}{
			"phase": "Active", "activeGroupState": "Trying",
			"observedGeneration": int64(2), "observedRevision": int64(3),
		},
	}}
}

func TestParseGrantOnlyUsesActiveGroup(t *testing.T) {
	g, err := parseGrant(testGrant())
	if err != nil {
		t.Fatalf("parseGrant returned error: %v", err)
	}
	if !g.valid(time.Now()) {
		t.Fatal("expected grant to be valid")
	}
	if g.nodes["node-a"] != types.UID("uid-a") {
		t.Fatalf("active group node missing: %#v", g.nodes)
	}
	if _, found := g.nodes["node-c"]; found {
		t.Fatal("inactive candidate group must not be authorized")
	}
}

func TestObservedRevisionMismatchIsInvalid(t *testing.T) {
	u := testGrant()
	_ = unstructured.SetNestedField(u.Object, int64(2), "status", "observedRevision")
	g, err := parseGrant(u)
	if err != nil {
		t.Fatalf("parseGrant returned error: %v", err)
	}
	if g.valid(time.Now()) {
		t.Fatal("stale observedRevision must fail closed")
	}
}
