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
	"strings"
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
	ResolvedSnapshot map[string]any
	StaticSnapshotID string
	TopologyConfig   []byte
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
	resolvedNodes := make([]any, 0, nodeCount)
	states := make([]any, 0, nodeCount)
	metrics := make(map[string]map[string]float64, nodeCount)
	nodes := make([]corev1.Node, 0, nodeCount)
	pods := []corev1.Pod{}
	leafCount := nodeCount / 20
	for number := 1; number <= nodeCount; number++ {
		leafNumber := (number-1)/20 + 1
		borderNumber := (leafNumber-1)*4/leafCount + 1
		name := fmt.Sprintf("worker-%04d", number)
		uid := fmt.Sprintf("uid-worker-%04d", number)
		leaf := fmt.Sprintf("leaf-%03d", leafNumber)
		borderDomain := fmt.Sprintf("HB-HL-DC1-102-BORDER-DOMAIN-%02d", borderNumber)
		borders := []string{
			fmt.Sprintf("HB-HL-DC1-102-BORDER-DOMAIN-%02d-SW01-ZTE9904X", borderNumber),
			fmt.Sprintf("HB-HL-DC1-102-BORDER-DOMAIN-%02d-SW02-ZTE9904X", borderNumber),
		}
		bandwidths := []float64{25, 40, 50, 100}
		latencies := []float64{4, 2.5, 1.5, 0.8}
		bandwidth := bandwidths[(leafNumber-1)%len(bandwidths)]
		latency := latencies[(leafNumber-1)%len(latencies)]
		leafIDs := []any{leaf}
		links := []any{map[string]any{
			"bondMode": "direct", "interface": "eth0", "leafSwitchId": leaf, "active": true,
		}}
		leafIDsJSON, _ := json.Marshal([]string{leaf})
		linksJSON, _ := json.Marshal(links)
		labels := map[string]string{
			"tests.ngg.io/worker":              "true",
			"topology.demo.ngg.io/leaf-switch": leaf,
			"topology.demo.ngg.io/leaf-set-id": leafSetShortID([]string{leaf}),
			"topology.demo.ngg.io/leaf-count":  "1",
		}
		topology := map[string]any{"leafSwitchId": leaf, "leafSwitchIds": leafIDs, "switchId": leaf, "links": links}
		resolvedTopology := map[string]any{
			"regionId": "CN-NORTH", "locationId": "HB-HL", "dataCenterId": "HB-HL-DC1",
			"roomId": "HB-HL-DC1-102", "borderDomainId": borderDomain,
			"borderSwitchIds": borders, "spineDomainId": "", "spineSwitchIds": []any{},
			"leafSwitchId": leaf, "leafSwitchIds": leafIDs, "switchId": leaf,
			"leafDomainId": leaf, "leafDomainSwitchIds": leafIDs, "peerLeafSwitchIds": []any{},
			"bandwidthGbps": bandwidth, "latencyMillis": latency,
		}
		staticNode := map[string]any{
			"nodeName": name, "nodeUID": uid, "createdAt": "2026-08-21T00:00:00Z",
			"allocatable": map[string]any{"cpu": "32", "memory": "128Gi", "nvidia.com/gpu": "4"},
			"labels":      labels, "topology": topology,
		}
		resolvedNode := copyMap(staticNode)
		resolvedNode["topology"] = resolvedTopology
		staticNodes = append(staticNodes, staticNode)
		resolvedNodes = append(resolvedNodes, resolvedNode)
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
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
			ObjectMeta: metav1.ObjectMeta{
				Name: name, UID: types.UID(uid), Labels: labels,
				Annotations: map[string]string{
					"topology.demo.ngg.io/leaf-switch-ids": string(leafIDsJSON),
					"topology.demo.ngg.io/leaf-links":      string(linksJSON),
				},
				CreationTimestamp: metav1.NewTime(time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)),
			},
			Spec:   corev1.NodeSpec{Unschedulable: inUse},
			Status: corev1.NodeStatus{Capacity: allocatable.DeepCopy(), Allocatable: allocatable.DeepCopy(), Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
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
	static := map[string]any{"clusterId": "mock-1000-node-cluster", "topologyVersion": "node-leaf-v1", "nodes": staticNodes}
	resolvedStatic := map[string]any{"clusterId": "mock-1000-node-cluster", "topologyVersion": "unicom-border-domain-v1", "nodes": resolvedNodes}
	topologyConfig, err := buildTopologyConfig(leafCount)
	if err != nil {
		return nil, err
	}
	snapshotID, err := CanonicalHash(static)
	if err != nil {
		return nil, err
	}
	ngdSpec := map[string]any{
		"schedulerName": "volcano",
		"nodeSelector":  map[string]any{"matchLabels": map[string]any{"tests.ngg.io/worker": "true"}},
		// 严格使用联通新版topologyLabels。spine-01在模拟拓扑中不存在，
		// Algorithm Go层会降级为Border requiredSame；Leaf requiredSame是更窄
		// 的硬约束，因此最终仍只允许从一个Leaf逻辑域选择Node。
		"topologyLabels": map[string]any{
			"topology.kubernetes.io/data-center":   "HB-HL-DC1",
			"topology.kubernetes.io/room":          "HB-HL-DC1-102",
			"topology.kubernetes.io/border-switch": "requiredSame",
			"topology.kubernetes.io/spine-switch":  "spine-01",
			"topology.kubernetes.io/leaf-switch":   "requiredSame",
		},
		// 每个Leaf有20个Node，其中约1/3处于占用状态；10个Node的最低需求
		// 可以在一个Leaf逻辑域内满足。
		"maxNodes":     int64(18),
		"quota":        map[string]any{"cpu": "576", "memory": "2304Gi"},
		"minResources": map[string]any{"cpu": "320", "memory": "1280Gi"},
	}
	allocation := map[string]any{
		"requestId": "group-request-1000", "taskUID": "task-uid-1000", "ngdUID": "ngd-uid-1000", "ngdGeneration": int64(1),
		"requestMode": "resourcePool", "nodeStaticSnapshotId": snapshotID, "nodeUsageStates": states, "ngd": ngdSpec, "debugTrace": true,
	}
	metricSnapshotID, err := CanonicalHash(map[string]any{"catalogueVersion": "node-exporter-network-v1", "nodes": metrics})
	if err != nil {
		return nil, err
	}
	workerStatic := copyMap(resolvedStatic)
	workerStatic["snapshotId"] = snapshotID
	workerRequest := copyMap(allocation)
	// Group1直接测试Go到Python Worker边界，因此输入使用Algorithm Go层已经
	// 解析好的内部逻辑域约束。其他组通过真实Algorithm HTTP服务自动生成它。
	workerRequest["topologyConstraints"] = map[string]any{
		"dataCenter": "HB-HL-DC1", "room": "HB-HL-DC1-102",
		"borderDomain": "requiredSame", "leafDomain": "requiredSame",
	}
	workerPayload := map[string]any{
		"request": workerRequest, "staticSnapshot": workerStatic,
		"metricSnapshot": map[string]any{
			"snapshotId": metricSnapshotID, "capturedAt": "2026-08-21T00:00:00Z", "capturedAtUnix": float64(1787270400),
			"catalogueVersion": "node-exporter-network-v1", "nodes": metrics,
		},
		"metricsDegraded": false,
		"warnings":        []any{"SPINE_NOT_FOUND_FALLBACK: Spine \"spine-01\" is absent from the configured topology; using existing border-switch constraint \"requiredSame\""},
	}
	demand := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "scheduling.platform.example.io/v1alpha1", "kind": "NodeGroupDemand",
		"metadata": map[string]any{"name": "go-test-demand", "labels": map[string]any{"scheduling.platform.example.io/scheduler": "volcano"}},
		"spec":     ngdSpec,
	}}
	return &Fixture{NodeCount: nodeCount, StaticSnapshot: static, ResolvedSnapshot: resolvedStatic, StaticSnapshotID: snapshotID, TopologyConfig: topologyConfig, NodeUsageStates: states, Metrics: metrics, Allocation: allocation, WorkerPayload: workerPayload, Nodes: nodes, Pods: pods, Demand: demand}, nil
}

func leafSetShortID(leaves []string) string {
	copyOfLeaves := append([]string(nil), leaves...)
	sort.Strings(copyOfLeaves)
	sum := sha256.Sum256([]byte(strings.Join(copyOfLeaves, "\x00")))
	return hex.EncodeToString(sum[:6])
}

// buildTopologyConfig生成与联通样例同构的独立配置：机房下每个Leaf包含
// SPINE/BORDER/LEAF邻接表，SPINE允许为空，双Border由显式Domain归并。
func buildTopologyConfig(leafCount int) ([]byte, error) {
	borderDomains := map[string]any{}
	room := map[string]any{}
	leafMetrics := map[string]any{}
	for number := 1; number <= leafCount; number++ {
		leaf := fmt.Sprintf("leaf-%03d", number)
		domainNumber := (number-1)*4/leafCount + 1
		domain := fmt.Sprintf("HB-HL-DC1-102-BORDER-DOMAIN-%02d", domainNumber)
		borders := []any{
			fmt.Sprintf("HB-HL-DC1-102-BORDER-DOMAIN-%02d-SW01-ZTE9904X", domainNumber),
			fmt.Sprintf("HB-HL-DC1-102-BORDER-DOMAIN-%02d-SW02-ZTE9904X", domainNumber),
		}
		members := make([]any, len(borders))
		copy(members, borders)
		borderDomains[domain] = map[string]any{"roomId": "HB-HL-DC1-102", "mode": "exact-set", "members": members}
		borderLinks := map[string]any{}
		for index, raw := range borders {
			borderLinks[raw.(string)] = map[string]any{
				"local_port": fmt.Sprintf("cgei-0/1/1/%d", 51+index*2),
				"peer_port":  fmt.Sprintf("cgei-0/3/0/%d", number),
			}
		}
		room[leaf] = map[string]any{"SPINE": map[string]any{}, "BORDER": borderLinks, "LEAF": map[string]any{}}
		bandwidths := []float64{25, 40, 50, 100}
		latencies := []float64{4, 2.5, 1.5, 0.8}
		leafMetrics[leaf] = map[string]any{
			"bandwidthGbps": bandwidths[(number-1)%len(bandwidths)],
			"latencyMillis": latencies[(number-1)%len(latencies)],
		}
	}
	config := map[string]any{
		"version": "unicom-border-domain-v1",
		"scopes": map[string]any{
			"regions":     []any{map[string]any{"id": "CN-NORTH", "name": "华北"}},
			"locations":   []any{map[string]any{"id": "HB-HL", "name": "怀来", "regionId": "CN-NORTH"}},
			"dataCenters": []any{map[string]any{"id": "HB-HL-DC1", "locationId": "HB-HL"}},
			"rooms":       []any{map[string]any{"id": "HB-HL-DC1-102", "dataCenterId": "HB-HL-DC1"}},
		},
		"borderDomains": borderDomains,
		"leafMetrics":   leafMetrics,
		"topology":      map[string]any{"HB-HL-DC1-102": room},
	}
	return yaml.Marshal(config)
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
		"node-static-snapshot.json":          static,
		"node-dynamic-state.json":            map[string]any{"scope": "request", "nodes": fixture.NodeUsageStates},
		"prometheus-metrics.json":            map[string]any{"source": "mock-prometheus-http", "nodes": fixture.Metrics},
		"allocation-request.json":            fixture.Allocation,
		"worker-payload.json":                fixture.WorkerPayload,
		"resolved-node-static-snapshot.json": fixture.ResolvedSnapshot,
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
	if err := os.WriteFile(filepath.Join(directory, "network-topology.yaml"), fixture.TopologyConfig, 0o644); err != nil {
		return err
	}
	return WriteJSON(filepath.Join(directory, "fixture-summary.json"), map[string]any{"nodeCount": fixture.NodeCount, "metricNamesPerNode": 14, "metricSamples": fixture.NodeCount * 14, "topology": "华北 / 怀来 / HB-HL-DC1 / 102机房 / 4 Border Domain / 50 Leaf / 20 Node per Leaf; SPINE empty"})
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
