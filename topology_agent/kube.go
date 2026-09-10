package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

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
	log.Printf("[LLDP-AGENT] KUBERNETES CLIENT SUCCESS apiServer=%q", config.Host)
	return &kubeClient{client: clientset}, nil
}

func (c *kubeClient) patchNode(ctx context.Context, name string, labels, annotations map[string]any) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("Kubernetes client is not initialized")
	}
	log.Printf("[LLDP-AGENT] CALL Kubernetes Nodes.Get node=%s purpose=pre-patch", name)
	current, err := c.client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get Node %s before patch: %w", name, err)
	}
	log.Printf("[LLDP-AGENT] RETURN Kubernetes Nodes.Get node=%s resourceVersion=%s existingLabels=%d existingAnnotations=%d", name, current.ResourceVersion, len(current.Labels), len(current.Annotations))
	body := map[string]any{"metadata": map[string]any{"labels": labels, "annotations": annotations}}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal Node metadata patch: %w", err)
	}
	log.Printf("[LLDP-AGENT] CALL Kubernetes Nodes.Patch node=%s type=MergePatch payloadBytes=%d", name, len(raw))
	updated, err := c.client.CoreV1().Nodes().Patch(ctx, name, types.MergePatchType, raw, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch Node %s: %w", name, err)
	}
	log.Printf("[LLDP-AGENT] RETURN Kubernetes Nodes.Patch node=%s resourceVersion=%s", name, updated.ResourceVersion)

	log.Printf("[LLDP-AGENT] CALL Kubernetes Nodes.Get node=%s purpose=post-patch-verification", name)
	verified, err := c.client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("re-read Node %s after patch: %w", name, err)
	}
	if err := verifyPatchedMetadata(verified.Labels, labels, "label"); err != nil {
		return fmt.Errorf("verify Node %s labels after patch: %w", name, err)
	}
	if err := verifyPatchedMetadata(verified.Annotations, annotations, "annotation"); err != nil {
		return fmt.Errorf("verify Node %s annotations after patch: %w", name, err)
	}
	log.Printf("[LLDP-AGENT] VERIFIED Kubernetes Node metadata node=%s resourceVersion=%s labels=%d annotations=%d", name, verified.ResourceVersion, len(labels), len(annotations))
	return nil
}

func verifyPatchedMetadata(actual map[string]string, desired map[string]any, kind string) error {
	for key, raw := range desired {
		if raw == nil {
			if value, exists := actual[key]; exists {
				return fmt.Errorf("managed %s %q should be absent, got %q", kind, key, value)
			}
			continue
		}
		want, ok := raw.(string)
		if !ok {
			return fmt.Errorf("managed %s %q has unsupported desired type %T", kind, key, raw)
		}
		got, exists := actual[key]
		if !exists {
			return fmt.Errorf("managed %s %q is missing", kind, key)
		}
		if got != want {
			return fmt.Errorf("managed %s %q mismatch: got %q, want %q", kind, key, got, want)
		}
	}
	return nil
}
