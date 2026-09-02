// Package common provides deterministic 3000-Node fixtures and reporting for
// the four scale benchmark groups.
package common

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	base "demo.ngg/go-test-suites/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	NodeCount      = 3000
	DefaultSamples = 30
	DefaultWarmups = 3
	ClusterID      = "mock-3000-node-cluster"
)

var DefaultTargets = []int{1000, 800, 500, 300, 100, 10}

func ScaleRoot() string {
	_, source, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(source), ".."))
}

// Fixture is the immutable benchmark source. All Nodes are Ready and
// schedulable, with no bound Pods, so minResources can select an exact count.
type Fixture struct {
	StaticSnapshot   map[string]any
	ResolvedSnapshot map[string]any
	StaticSnapshotID string
	TopologyConfig   []byte
	NodeUsageStates  []any
	Metrics          map[string]map[string]float64
	MetricSnapshot   map[string]any
	Nodes            []corev1.Node
}

// GenerateFixture scales the existing deterministic topology generator to
// 3000 Nodes, then removes dynamic unavailability used by functional tests.
func GenerateFixture() (*Fixture, error) {
	source, err := base.GenerateFixture(NodeCount)
	if err != nil {
		return nil, err
	}
	states := make([]any, 0, len(source.Nodes))
	nodes := make([]corev1.Node, len(source.Nodes))
	for index := range source.Nodes {
		nodes[index] = *source.Nodes[index].DeepCopy()
		nodes[index].Spec.Unschedulable = false
		states = append(states, map[string]any{
			"nodeUID":            nodes[index].UID,
			"inUse":              false,
			"requestedResources": map[string]any{},
		})
	}
	metric, ok := source.WorkerPayload["metricSnapshot"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("base fixture has no metricSnapshot")
	}
	static := copyMap(source.StaticSnapshot)
	static["clusterId"] = ClusterID
	resolved := copyMap(source.ResolvedSnapshot)
	resolved["clusterId"] = ClusterID
	staticID, err := base.CanonicalHash(static)
	if err != nil {
		return nil, err
	}
	return &Fixture{
		StaticSnapshot: static, ResolvedSnapshot: resolved, StaticSnapshotID: staticID, TopologyConfig: source.TopologyConfig,
		NodeUsageStates: states, Metrics: source.Metrics,
		MetricSnapshot: copyMap(metric), Nodes: nodes,
	}, nil
}

// DemandSpec makes minResources, quota and maxNodes converge on exactly target
// Nodes because every benchmark Node has 32 CPU and 128Gi available.
func DemandSpec(target int) map[string]any {
	cpu := strconv.Itoa(target * 32)
	memory := fmt.Sprintf("%dGi", target*128)
	return map[string]any{
		"schedulerName": "volcano",
		"nodeSelector":  map[string]any{"matchLabels": map[string]any{"tests.ngg.io/worker": "true"}},
		"maxNodes":      int64(target),
		"quota":         map[string]any{"cpu": cpu, "memory": memory},
		"minResources":  map[string]any{"cpu": cpu, "memory": memory},
	}
}

func (f *Fixture) Allocation(target int, requestID string) map[string]any {
	return map[string]any{
		"requestId": requestID, "taskUID": requestID + "-task", "ngdUID": requestID + "-ngd", "ngdGeneration": int64(1),
		"requestMode": "resourcePool", "nodeStaticSnapshotId": f.StaticSnapshotID,
		"nodeUsageStates": f.NodeUsageStates, "ngd": DemandSpec(target), "debugTrace": false,
	}
}

func (f *Fixture) WorkerPayload(target int, requestID string) map[string]any {
	static := copyMap(f.ResolvedSnapshot)
	static["clusterId"] = ClusterID
	static["snapshotId"] = f.StaticSnapshotID
	return map[string]any{
		"request": f.Allocation(target, requestID), "staticSnapshot": static,
		"metricSnapshot": f.MetricSnapshot, "metricsDegraded": false, "warnings": []any{},
	}
}

func (f *Fixture) Demand(target int, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "scheduling.platform.example.io/v1alpha1", "kind": "NodeGroupDemand",
		"metadata": map[string]any{"name": name, "labels": map[string]any{"scheduling.platform.example.io/scheduler": "volcano"}},
		"spec":     DemandSpec(target),
	}}
}

// WriteCanonicalInputs materializes one shared 3000-Node input and six NGDs.
func WriteCanonicalInputs(root string, fixture *Fixture) error {
	input := filepath.Join(root, "testdata", "input")
	static := copyMap(fixture.StaticSnapshot)
	static["snapshotId"] = fixture.StaticSnapshotID
	if err := base.WriteJSON(filepath.Join(input, "node-static-snapshot-3000.json"), static); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(input, "network-topology.yaml"), fixture.TopologyConfig, 0o644); err != nil {
		return err
	}
	if err := base.WriteJSON(filepath.Join(input, "node-dynamic-state-3000.json"), map[string]any{"scope": "request", "nodes": fixture.NodeUsageStates}); err != nil {
		return err
	}
	if err := base.WriteJSON(filepath.Join(input, "prometheus-metrics-3000.json"), map[string]any{"source": "mock-prometheus-http", "nodes": fixture.Metrics}); err != nil {
		return err
	}
	if err := base.WriteJSON(filepath.Join(input, "fixture-summary.json"), map[string]any{
		"nodeCount": NodeCount, "regionCount": 1, "locationCount": 1, "dataCenterCount": 1, "roomCount": 1, "borderDomainCount": 4, "leafCount": 150,
		"nodesPerLeaf": 20, "metricNamesPerNode": 14, "metricSamples": NodeCount * 14,
	}); err != nil {
		return err
	}
	for _, target := range DefaultTargets {
		demand := (&Fixture{}).Demand(target, fmt.Sprintf("benchmark-select-%d", target))
		if err := base.WriteYAML(filepath.Join(input, "demands", fmt.Sprintf("select-%d.yaml", target)), demand.Object); err != nil {
			return err
		}
	}
	return nil
}

func Targets() ([]int, error) {
	raw := strings.TrimSpace(os.Getenv("BENCHMARK_TARGETS"))
	if raw == "" {
		return append([]int(nil), DefaultTargets...), nil
	}
	seen := map[int]struct{}{}
	result := []int{}
	for _, item := range strings.Split(raw, ",") {
		value, err := strconv.Atoi(strings.TrimSpace(item))
		if err != nil || value < 1 || value > 1000 {
			return nil, fmt.Errorf("BENCHMARK_TARGETS contains invalid target %q", item)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func Samples() (int, error) { return positiveEnv("BENCHMARK_SAMPLES", DefaultSamples) }
func Warmups() (int, error) { return nonNegativeEnv("BENCHMARK_WARMUPS", DefaultWarmups) }

func positiveEnv(name string, fallback int) (int, error) {
	value, err := nonNegativeEnv(name, fallback)
	if err != nil {
		return 0, err
	}
	if value < 1 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return value, nil
}

func nonNegativeEnv(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return value, nil
}

func copyMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

// SortedNodeNames returns stable names for mock selection and assertions.
func SortedNodeNames(nodes []corev1.Node) []string {
	names := make([]string, 0, len(nodes))
	for index := range nodes {
		names = append(names, nodes[index].Name)
	}
	sort.Strings(names)
	return names
}
