package common

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"scheduling.demo.ngg.io/prc/pkg/controller"
)

func ValidateGenericResult(result map[string]any, target int) error {
	rawGroups, ok := result["candidateNodeGroups"].([]any)
	if !ok || len(rawGroups) == 0 || len(rawGroups) > 3 {
		return fmt.Errorf("candidate groups=%d, want 1..3", len(rawGroups))
	}
	for groupIndex, rawGroup := range rawGroups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			return fmt.Errorf("candidate group %d has invalid type", groupIndex)
		}
		rank, _ := number(group["rank"])
		if int(rank) != groupIndex+1 {
			return fmt.Errorf("rank=%v, want %d", group["rank"], groupIndex+1)
		}
		nodes, ok := group["nodes"].([]any)
		if !ok || len(nodes) != target {
			return fmt.Errorf("group %d nodes=%d, want %d", groupIndex+1, len(nodes), target)
		}
		seen := map[string]struct{}{}
		for _, rawNode := range nodes {
			node, _ := rawNode.(map[string]any)
			name := fmt.Sprint(node["nodeName"])
			if name == "" {
				return fmt.Errorf("group %d contains empty nodeName", groupIndex+1)
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("group %d contains duplicate Node %s", groupIndex+1, name)
			}
			seen[name] = struct{}{}
		}
	}
	return nil
}

func ValidateResponse(response controller.AlgorithmResponse, target int) error {
	if response.Status != "SUCCESS" {
		return fmt.Errorf("status=%s, want SUCCESS", response.Status)
	}
	if len(response.CandidateNodeGroups) == 0 || len(response.CandidateNodeGroups) > 3 {
		return fmt.Errorf("candidate groups=%d, want 1..3", len(response.CandidateNodeGroups))
	}
	for index, group := range response.CandidateNodeGroups {
		if group.Rank != int64(index+1) {
			return fmt.Errorf("rank=%d, want %d", group.Rank, index+1)
		}
		if len(group.Nodes) != target {
			return fmt.Errorf("group %d nodes=%d, want %d", index+1, len(group.Nodes), target)
		}
		seen := map[string]struct{}{}
		for _, node := range group.Nodes {
			if _, exists := seen[node.NodeName]; exists {
				return fmt.Errorf("group %d contains duplicate Node %s", index+1, node.NodeName)
			}
			seen[node.NodeName] = struct{}{}
		}
	}
	return nil
}

func ValidateGrant(grant *unstructured.Unstructured, target int) error {
	nodes, found, err := unstructured.NestedSlice(grant.Object, "spec", "nodes")
	if err != nil || !found {
		return fmt.Errorf("NGG spec.nodes missing: %v", err)
	}
	if len(nodes) != target {
		return fmt.Errorf("NGG nodes=%d, want %d", len(nodes), target)
	}
	seen := map[string]struct{}{}
	for _, raw := range nodes {
		node, _ := raw.(map[string]any)
		name := fmt.Sprint(node["name"])
		if name == "" {
			return fmt.Errorf("NGG contains empty Node name")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("NGG contains duplicate Node %s", name)
		}
		seen[name] = struct{}{}
	}
	phase, _, _ := unstructured.NestedString(grant.Object, "status", "phase")
	if phase != "Active" {
		return fmt.Errorf("NGG phase=%s, want Active", phase)
	}
	return nil
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}
