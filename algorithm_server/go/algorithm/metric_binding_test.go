package algorithm

import (
	"strings"
	"testing"
)

func TestBindMetricSnapshotToNodesByInternalIP(t *testing.T) {
	input := &metricSnapshot{Nodes: map[string]map[string]float64{
		"10.0.0.2": {"cpuUsageRatio": 0.2},
		"10.0.0.1": {"cpuUsageRatio": 0.1},
	}}
	static := staticSnapshot{Nodes: []map[string]any{
		{"nodeName": "worker-1", "nodeIP": "10.0.0.1"},
		{"nodeName": "worker-2", "nodeIP": "10.0.0.2"},
	}}

	got, degraded, warnings := bindMetricSnapshotToNodes(input, static, prometheusNodeIPLabel)
	if degraded || len(warnings) != 0 {
		t.Fatalf("degraded=%v warnings=%v", degraded, warnings)
	}
	if got.Nodes["worker-1"]["cpuUsageRatio"] != 0.1 || got.Nodes["worker-2"]["cpuUsageRatio"] != 0.2 {
		t.Fatalf("mapped metrics=%v", got.Nodes)
	}
	if _, found := got.Nodes["10.0.0.1"]; found {
		t.Fatalf("raw IP identity leaked into Worker metrics: %v", got.Nodes)
	}
	if _, found := input.Nodes["10.0.0.1"]; !found {
		t.Fatal("binding mutated the cached metric snapshot")
	}
}

func TestBindMetricSnapshotToNodesReportsMissingIdentity(t *testing.T) {
	input := &metricSnapshot{Nodes: map[string]map[string]float64{
		"10.0.0.9": {"cpuUsageRatio": 0.9},
	}}
	static := staticSnapshot{Nodes: []map[string]any{
		{"nodeName": "worker-1", "nodeIP": "10.0.0.1"},
		{"nodeName": "worker-2"},
	}}

	got, degraded, warnings := bindMetricSnapshotToNodes(input, static, prometheusNodeIPLabel)
	if !degraded || len(got.Nodes) != 0 {
		t.Fatalf("degraded=%v mapped=%v", degraded, got.Nodes)
	}
	joined := strings.Join(warnings, "\n")
	for _, expected := range []string{"no Kubernetes InternalIP", "absent from the static snapshot", "metrics are missing"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("warnings=%q, missing %q", joined, expected)
		}
	}
}

func TestBindMetricSnapshotToNodesKeepsNodeNameCatalogue(t *testing.T) {
	input := &metricSnapshot{Nodes: map[string]map[string]float64{
		"worker-1": {"cpuUsageRatio": 0.1},
	}}
	got, degraded, warnings := bindMetricSnapshotToNodes(input, staticSnapshot{}, "node")
	if got != input || degraded || warnings != nil {
		t.Fatalf("node-name catalogue changed: got=%p input=%p degraded=%v warnings=%v", got, input, degraded, warnings)
	}
}
