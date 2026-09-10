package main

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPatchNodeUsesMergePatchAndVerifiesMetadata(t *testing.T) {
	const nodeName = "worker-01"
	clientset := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: nodeName,
		Labels: map[string]string{
			"unrelated.example/keep":    "yes",
			labelPrefix + "leaf-switch": "old-leaf",
		},
		Annotations: map[string]string{"unrelated.example/keep": "yes"},
	}})
	client := &kubeClient{client: clientset}
	labels := map[string]any{
		labelPrefix + "leaf-set-id": "abcdef123456",
		labelPrefix + "leaf-count":  "2",
		labelPrefix + "leaf-switch": nil,
	}
	annotations := map[string]any{
		labelPrefix + "leaf-switch-ids": `["leaf-a","leaf-b"]`,
		labelPrefix + "source":          "LLDP",
	}

	if err := client.patchNode(context.Background(), nodeName, labels, annotations); err != nil {
		t.Fatal(err)
	}
	got, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Labels["unrelated.example/keep"] != "yes" || got.Annotations["unrelated.example/keep"] != "yes" {
		t.Fatalf("unrelated metadata was changed: labels=%v annotations=%v", got.Labels, got.Annotations)
	}
	if _, exists := got.Labels[labelPrefix+"leaf-switch"]; exists {
		t.Fatalf("obsolete single-Leaf label was not removed: %v", got.Labels)
	}
	if got.Labels[labelPrefix+"leaf-count"] != "2" || got.Annotations[labelPrefix+"source"] != "LLDP" {
		t.Fatalf("desired topology metadata was not persisted: labels=%v annotations=%v", got.Labels, got.Annotations)
	}
}

func TestVerifyPatchedMetadataDetectsMismatch(t *testing.T) {
	err := verifyPatchedMetadata(map[string]string{"example/key": "old"}, map[string]any{"example/key": "new"}, "label")
	if err == nil {
		t.Fatal("expected verification mismatch")
	}
}
