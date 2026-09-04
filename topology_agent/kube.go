package main

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type nodeObject struct {
	Metadata nodeMetadata `json:"metadata"`
}

type nodeMetadata struct {
	Name            string            `json:"name"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resourceVersion"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
}

// kubeClient wraps the official client-go client. Authentication,
// certificates and API endpoints therefore work identically for kubeconfig
// and in-cluster rest.Config values.
type kubeClient struct {
	client kubernetes.Interface
}

func newKubeClient(config *rest.Config) (*kubeClient, error) {
	if config == nil {
		return nil, fmt.Errorf("Kubernetes rest.Config is required")
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	return &kubeClient{client: clientset}, nil
}

func (c *kubeClient) getNode(ctx context.Context, name string) (nodeObject, error) {
	if c == nil || c.client == nil {
		return nodeObject{}, fmt.Errorf("Kubernetes client is not initialized")
	}
	node, err := c.client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nodeObject{}, err
	}
	return convertNode(node), nil
}

func (c *kubeClient) patchNode(ctx context.Context, name string, labels, annotations map[string]any) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("Kubernetes client is not initialized")
	}
	body := map[string]any{"metadata": map[string]any{"labels": labels, "annotations": annotations}}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal Node metadata patch: %w", err)
	}
	_, err = c.client.CoreV1().Nodes().Patch(ctx, name, types.MergePatchType, raw, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch Node %s: %w", name, err)
	}
	return nil
}

func convertNode(node *corev1.Node) nodeObject {
	if node == nil {
		return nodeObject{}
	}
	return nodeObject{Metadata: nodeMetadata{
		Name: node.Name, UID: string(node.UID), ResourceVersion: node.ResourceVersion,
		Labels: node.Labels, Annotations: node.Annotations,
	}}
}
