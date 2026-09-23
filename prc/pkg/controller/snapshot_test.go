package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestBuildStaticSnapshotIncludesInternalIP(t *testing.T) {
	node := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-1", UID: types.UID("uid-worker-1"),
			Labels: map[string]string{"topology.demo.ngg.io/leaf-switch": "leaf-1"},
		},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeHostName, Address: "worker-1"},
			{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
		}},
	}
	_, snapshot, err := buildStaticSnapshot("cluster-1", []corev1.Node{node})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Nodes) != 1 || snapshot.Nodes[0].NodeIP != "10.0.0.1" {
		t.Fatalf("snapshot Nodes=%+v", snapshot.Nodes)
	}
}
