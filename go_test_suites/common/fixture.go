package common

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
)

// Fixture包含四组测试共用的确定性1000 Node输入。
type Fixture struct {
	NodeCount        int
	StaticSnapshot   map[string]any
	StaticSnapshotID string
	NodeUsageStates  []any
	Metrics          map[string]map[string]float64
	Allocation       map[string]any
	WorkerPayload    map[string]any
	Nodes            []corev1.Node
	Pods             []corev1.Pod
	Demand           *unstructured.Unstructured
}

// GenerateFixture用固定公式生成1000 Node、三层拓扑、动态状态和14项指标。
func GenerateFixture(nodeCount int) (*Fixture, error) {
	if nodeCount <= 0 || nodeCount%20 != 0 {
		return nil, fmt.Errorf("nodeCount must be a positive multiple of 20")
	}
	staticNodes := make([]any, 0, nodeCount)
	states := make([]any, 0, nodeCount)
	metrics := make(map[string]map[string]float64, nodeCount)
	nodes := make([]corev1.Node, 0, nodeCount)
	pods := []corev1.Pod{}
	leafCount := nodeCount / 20
	for number := 1; number <= nodeCount; number++ {
		leafNumber := (number-1)/20 + 1
		borderNumber := (leafNumber-1)*4/leafCount + 1
		coreNumber := (borderNumber-1)/2 + 1
		name := fmt.Sprintf("worker-%04d", number)
		uid := fmt.Sprintf("uid-worker-%04d", number)
		leaf := fmt.Sprintf("leaf-%03d", leafNumber)
		border := fmt.Sprintf("border-%02d", borderNumber)
		core := fmt.Sprintf("core-%02d", coreNumber)
		bandwidths := []float64{25, 40, 50, 100}
		latencies := []float64{4, 2.5, 1.5, 0.8}
		bandwidth := bandwidths[(leafNumber-1)%len(bandwidths)]
		latency := latencies[(leafNumber-1)%len(latencies)]
		labels := map[string]string{
			"tests.ngg.io/worker":                     "true",
			"topology.kubernetes.io/region":           "dc-test-01",
			"topology.kubernetes.io/rack":             fmt.Sprintf("rack-%03d", leafNumber),
			"topology.demo.ngg.io/topology-version":   "dc-core-border-leaf-v1",
			"topology.demo.ngg.io/core-switch":        core,
			"topology.demo.ngg.io/border-switch":      border,
			"topology.demo.ngg.io/leaf-switch":        leaf,
			"topology.demo.ngg.io/bandwidth-gbps":     fmt.Sprint(bandwidth),
			"topology.demo.ngg.io/latency-ms":         fmt.Sprint(latency),
			"topology.demo.ngg.io/convergence-switch": border,
		}
		topology := map[string]any{
			"dataCenter": "dc-test-01", "coreSwitchId": core, "borderSwitchId": border,
			"leafSwitchId": leaf, "switchId": leaf, "bandwidthGbps": bandwidth, "latencyMillis": latency,
		}
		staticNodes = append(staticNodes, map[string]any{
			"nodeName": name, "nodeUID": uid, "createdAt": "2026-08-21T00:00:00Z",
			"allocatable": map[string]any{"cpu": "32", "memory": "128Gi", "nvidia.com/gpu": "4"},
			"labels":      labels, "topology": topology,
		})
		inUse := number%3 == 0
		state := map[string]any{"nodeUID": uid, "inUse": inUse, "requestedResources": map[string]any{}}
		states = append(states, state)
		cpu := minFloat(0.95, 0.08+float64((leafNumber*13+number*7)%65)/100)
		memory := minFloat(0.95, 0.10+float64((leafNumber*11+number*5)%60)/100)
		receiveBPS := float64(20_000_000 + (number*7919)%600_000_000)
		transmitBPS := float64(15_000_000 + (number*6151)%500_000_000)
		receivePPS := float64(20_000 + (number*97)%800_000)
		transmitPPS := float64(18_000 + (number*89)%700_000)
		utilization := minFloat(0.95, (receiveBPS+transmitBPS)/(bandwidth*125_000_000))
		metrics[name] = map[string]float64{
			"cpuUsageRatio": cpu, "memoryUsageRatio": memory,
			"networkReceiveBytesPerSecond": receiveBPS, "networkTransmitBytesPerSecond": transmitBPS,
			"networkReceivePacketsPerSecond": receivePPS, "networkTransmitPacketsPerSecond": transmitPPS,
			"networkReceiveDropRatio":   float64(number%11) / 10000,
			"networkTransmitDropRatio":  float64(number%13) / 10000,
			"networkReceiveErrorRatio":  float64(number%7) / 20000,
			"networkTransmitErrorRatio": float64(number%5) / 20000,
			"tcpRetransmitRatio":        float64(number%17) / 10000,
			"networkLinkUpRatio": func() float64 {
				if number%101 == 0 {
					return 0.5
				}
				return 1
			}(),
			"networkUtilizationRatio":          utilization,
			"availableBandwidthBytesPerSecond": maxFloat(0, bandwidth*125_000_000-receiveBPS-transmitBPS),
		}
		allocatable := corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("32"), corev1.ResourceMemory: resource.MustParse("128Gi"),
			corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("4"),
		}
		nodes = append(nodes, corev1.Node{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
			ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid), Labels: labels, CreationTimestamp: metav1.NewTime(time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC))},
			Spec:       corev1.NodeSpec{Unschedulable: inUse},
			Status:     corev1.NodeStatus{Capacity: allocatable.DeepCopy(), Allocatable: allocatable.DeepCopy(), Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
		})
		if number%100 == 1 {
			pods = append(pods, corev1.Pod{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("bound-load-%04d", number), Namespace: "ngd-ngg-test"},
				Spec:       corev1.PodSpec{NodeName: name, Containers: []corev1.Container{{Name: "load", Image: "fixture.invalid/load:never-run", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")}}}}},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning},
			})
		}
	}
	static := map[string]any{"clusterId": "mock-1000-node-cluster", "topologyVersion": "dc-core-border-leaf-v1", "nodes": staticNodes}
	snapshotID, err := CanonicalHash(static)
	if err != nil {
		return nil, err
	}
	ngdSpec := map[string]any{
		"schedulerName":       "volcano",
		"nodeSelector":        map[string]any{"matchLabels": map[string]any{"tests.ngg.io/worker": "true"}},
		"topologyRequirement": map[string]any{"profile": "leaf-border-core-v1", "strategy": "NarrowestFit", "widestAllowedLevel": "coreSwitch"},
		"maxCandidateGroups":  int64(3), "maxNodes": int64(6),
		"quota":        map[string]any{"cpu": "192", "memory": "768Gi"},
		"minResources": map[string]any{"cpu": "96", "memory": "384Gi"},
	}
	allocation := map[string]any{
		"requestId": "group-request-1000", "taskUID": "task-uid-1000", "ngdUID": "ngd-uid-1000", "ngdGeneration": int64(1),
		"requestMode": "resourcePool", "nodeStaticSnapshotId": snapshotID, "nodeUsageStates": states, "ngd": ngdSpec, "debugTrace": true,
	}
	metricSnapshotID, err := CanonicalHash(map[string]any{"catalogueVersion": "node-exporter-network-v1", "nodes": metrics})
	if err != nil {
		return nil, err
	}
	workerStatic := copyMap(static)
	workerStatic["snapshotId"] = snapshotID
	workerPayload := map[string]any{
		"request": allocation, "staticSnapshot": workerStatic,
		"metricSnapshot": map[string]any{
			"snapshotId": metricSnapshotID, "capturedAt": "2026-08-21T00:00:00Z", "capturedAtUnix": float64(1787270400),
			"catalogueVersion": "node-exporter-network-v1", "nodes": metrics,
		},
		"metricsDegraded": false, "warnings": []any{},
	}
	demand := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "scheduling.platform.example.io/v1alpha1", "kind": "NodeGroupDemand",
		"metadata": map[string]any{"name": "go-test-demand", "labels": map[string]any{"scheduling.platform.example.io/scheduler": "volcano"}},
		"spec":     ngdSpec,
	}}
	return &Fixture{NodeCount: nodeCount, StaticSnapshot: static, StaticSnapshotID: snapshotID, NodeUsageStates: states, Metrics: metrics, Allocation: allocation, WorkerPayload: workerPayload, Nodes: nodes, Pods: pods, Demand: demand}, nil
}

// CanonicalHash与Algorithm静态缓存的JSON SHA-256规则一致。
func CanonicalHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func WriteJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func WriteYAML(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func ReadJSON(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(target)
}

// WriteFixtureInput将完整模拟输入写到指定组的testdata/input供现场查看。
func WriteFixtureInput(directory string, fixture *Fixture, includeKubernetes bool) error {
	static := copyMap(fixture.StaticSnapshot)
	static["snapshotId"] = fixture.StaticSnapshotID
	files := map[string]any{
		"node-static-snapshot.json": static,
		"node-dynamic-state.json":   map[string]any{"scope": "request", "nodes": fixture.NodeUsageStates},
		"prometheus-metrics.json":   map[string]any{"source": "mock-prometheus-http", "nodes": fixture.Metrics},
		"allocation-request.json":   fixture.Allocation,
		"worker-payload.json":       fixture.WorkerPayload,
	}
	if includeKubernetes {
		files["kubernetes-nodes.json"] = map[string]any{"items": fixture.Nodes}
		files["kubernetes-pods.json"] = map[string]any{"items": fixture.Pods}
		if err := WriteYAML(filepath.Join(directory, "ngd.yaml"), fixture.Demand.Object); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(files))
	for name := range files {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if err := WriteJSON(filepath.Join(directory, name), files[name]); err != nil {
			return err
		}
	}
	return WriteJSON(filepath.Join(directory, "fixture-summary.json"), map[string]any{"nodeCount": fixture.NodeCount, "metricNamesPerNode": 14, "metricSamples": fixture.NodeCount * 14, "topology": "2 core / 4 border / 50 leaf / 20 nodes per leaf"})
}

func copyMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func minFloat(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}
func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}
