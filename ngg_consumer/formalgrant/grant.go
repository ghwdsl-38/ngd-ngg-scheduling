// Package formalgrant parses and merges the formal flat NodeGroupGrant API.
package formalgrant

import (
	"fmt"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Node struct {
	Name  string
	Score int64
}

type Grant struct {
	Name          string
	Generation    int64
	SchedulerName string
	Version       string
	Source        string
	Timestamp     time.Time
	Nodes         []Node
}

// Parse validates one formal NGG and preserves spec.nodes order.
func Parse(object *unstructured.Unstructured, schedulerName string, now time.Time, expiry time.Duration) (Grant, error) {
	if object == nil {
		return Grant{}, fmt.Errorf("NGG is nil")
	}
	phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
	if phase != "Active" {
		return Grant{}, fmt.Errorf("NGG %s phase is %q", object.GetName(), phase)
	}
	target, _, _ := unstructured.NestedString(object.Object, "spec", "schedulerName")
	if target != schedulerName {
		return Grant{}, fmt.Errorf("NGG %s targets scheduler %q", object.GetName(), target)
	}
	version, _, _ := unstructured.NestedString(object.Object, "spec", "version")
	if version != "v1" {
		return Grant{}, fmt.Errorf("NGG %s has unsupported version %q", object.GetName(), version)
	}
	source, _, _ := unstructured.NestedString(object.Object, "spec", "source")
	if source != "normal" && source != "degraded" && source != "disabled" {
		return Grant{}, fmt.Errorf("NGG %s has invalid source %q", object.GetName(), source)
	}
	rawTimestamp, _, _ := unstructured.NestedString(object.Object, "spec", "timestamp")
	timestamp, err := time.Parse(time.RFC3339Nano, rawTimestamp)
	if err != nil {
		return Grant{}, fmt.Errorf("NGG %s has invalid timestamp: %w", object.GetName(), err)
	}
	if expiry > 0 && now.Sub(timestamp) > expiry {
		return Grant{}, fmt.Errorf("NGG %s is expired", object.GetName())
	}
	items, found, err := unstructured.NestedSlice(object.Object, "spec", "nodes")
	if err != nil || !found || len(items) == 0 {
		return Grant{}, fmt.Errorf("NGG %s has no nodes", object.GetName())
	}
	seen := map[string]struct{}{}
	nodes := make([]Node, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return Grant{}, fmt.Errorf("NGG %s contains malformed node", object.GetName())
		}
		name, _ := item["name"].(string)
		score, ok := item["score"].(int64)
		if !ok || name == "" || score < 0 || score > 100 {
			return Grant{}, fmt.Errorf("NGG %s contains invalid node name/score", object.GetName())
		}
		if _, duplicate := seen[name]; duplicate {
			return Grant{}, fmt.Errorf("NGG %s repeats node %s", object.GetName(), name)
		}
		seen[name] = struct{}{}
		nodes = append(nodes, Node{Name: name, Score: score})
	}
	return Grant{Name: object.GetName(), Generation: object.GetGeneration(), SchedulerName: target, Version: version, Source: source, Timestamp: timestamp, Nodes: nodes}, nil
}

// Merge combines valid grants; duplicate Nodes retain the highest score.
func Merge(grants []Grant) []Node {
	byName := map[string]int64{}
	for _, grant := range grants {
		for _, node := range grant.Nodes {
			if score, found := byName[node.Name]; !found || node.Score > score {
				byName[node.Name] = node.Score
			}
		}
	}
	result := make([]Node, 0, len(byName))
	for name, score := range byName {
		result = append(result, Node{Name: name, Score: score})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Score == result[j].Score {
			return result[i].Name < result[j].Name
		}
		return result[i].Score > result[j].Score
	})
	return result
}
