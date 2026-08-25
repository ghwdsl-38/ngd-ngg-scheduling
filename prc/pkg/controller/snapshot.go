package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

type staticNode struct {
	NodeName    string            `json:"nodeName"`
	NodeUID     string            `json:"nodeUID"`
	CreatedAt   string            `json:"createdAt"`
	Allocatable map[string]string `json:"allocatable"`
	Labels      map[string]string `json:"labels"`
	Topology    topology          `json:"topology"`
}

type topology struct {
	SwitchID       string  `json:"switchId"`
	LeafSwitchID   string  `json:"leafSwitchId"`
	BorderSwitchID string  `json:"borderSwitchId,omitempty"`
	CoreSwitchID   string  `json:"coreSwitchId"`
	BandwidthGbps  float64 `json:"bandwidthGbps"`
	LatencyMillis  float64 `json:"latencyMillis"`
}

type staticSnapshot struct {
	ClusterID       string       `json:"clusterId"`
	TopologyVersion string       `json:"topologyVersion"`
	Nodes           []staticNode `json:"nodes"`
}

type schedulerNodeState struct {
	NodeName           string            `json:"nodeName"`
	NodeUID            string            `json:"nodeUID"`
	Ready              bool              `json:"ready"`
	Unschedulable      bool              `json:"unschedulable"`
	RequestedResources map[string]string `json:"requestedResources"`
}

func buildStaticSnapshot(clusterID string, nodes []corev1.Node, topologies []unstructured.Unstructured) (string, staticSnapshot, error) {
	byUID := map[string]topology{}
	versions := map[string]struct{}{}
	for i := range topologies {
		item := &topologies[i]
		uid, _, _ := unstructured.NestedString(item.Object, "spec", "nodeRef", "uid")
		switchID, _, _ := unstructured.NestedString(item.Object, "status", "switchId")
		if uid == "" || switchID == "" {
			continue
		}
		coreSwitch, _, _ := unstructured.NestedString(item.Object, "status", "coreSwitchId")
		bandwidth := nestedNumber(item.Object, "status", "bandwidthGbps")
		latency := nestedNumber(item.Object, "status", "latencyMillis")
		version, _, _ := unstructured.NestedString(item.Object, "status", "topologyVersion")
		byUID[uid] = topology{SwitchID: switchID, LeafSwitchID: switchID, CoreSwitchID: coreSwitch, BandwidthGbps: bandwidth, LatencyMillis: latency}
		if version != "" {
			versions[version] = struct{}{}
		}
	}
	result := staticSnapshot{ClusterID: clusterID}
	for i := range nodes {
		node := &nodes[i]
		topo, topologyVersion, found := topologyFromNodeLabels(node.Labels)
		if found {
			versions[topologyVersion] = struct{}{}
		} else {
			topo, found = byUID[string(node.UID)]
		}
		if !found {
			continue
		}
		allocatable := map[string]string{}
		for name, quantity := range node.Status.Allocatable {
			allocatable[string(name)] = quantity.String()
		}
		result.Nodes = append(result.Nodes, staticNode{
			NodeName: node.Name, NodeUID: string(node.UID), CreatedAt: node.CreationTimestamp.UTC().Format(time.RFC3339),
			Allocatable: allocatable, Labels: node.Labels, Topology: topo,
		})
	}
	sort.Slice(result.Nodes, func(i, j int) bool {
		if result.Nodes[i].NodeUID == result.Nodes[j].NodeUID {
			return result.Nodes[i].NodeName < result.Nodes[j].NodeName
		}
		return result.Nodes[i].NodeUID < result.Nodes[j].NodeUID
	})
	versionList := make([]string, 0, len(versions))
	for version := range versions {
		versionList = append(versionList, version)
	}
	sort.Strings(versionList)
	result.TopologyVersion = strings.Join(versionList, "+")
	if result.TopologyVersion == "" {
		result.TopologyVersion = "unknown"
	}
	if len(result.Nodes) == 0 {
		return "", result, fmt.Errorf("no Worker has usable topology labels or NodeNetworkTopology status")
	}
	id, err := contentHash(result)
	return id, result, err
}

// BuildStaticSnapshot使用与Reconciler相同的生产逻辑生成内容Hash和可序列化快照。
func BuildStaticSnapshot(clusterID string, nodes []corev1.Node, topologies []unstructured.Unstructured) (string, any, error) {
	id, snapshot, err := buildStaticSnapshot(clusterID, nodes, topologies)
	return id, snapshot, err
}

func topologyFromNodeLabels(labels map[string]string) (topology, string, bool) {
	const prefix = "topology.demo.ngg.io/"
	leaf := labels[prefix+"leaf-switch"]
	if leaf == "" {
		leaf = labels[prefix+"switch"]
	}
	core := labels[prefix+"core-switch"]
	version := labels[prefix+"topology-version"]
	if leaf == "" || core == "" || version == "" {
		return topology{}, "", false
	}
	bandwidth, bandwidthErr := strconv.ParseFloat(labels[prefix+"bandwidth-gbps"], 64)
	latency, latencyErr := strconv.ParseFloat(labels[prefix+"latency-ms"], 64)
	if bandwidthErr != nil || latencyErr != nil {
		return topology{}, "", false
	}
	return topology{
		SwitchID: leaf, LeafSwitchID: leaf,
		BorderSwitchID: labels[prefix+"border-switch"], CoreSwitchID: core,
		BandwidthGbps: bandwidth, LatencyMillis: latency,
	}, version, true
}

func nestedNumber(object map[string]any, fields ...string) float64 {
	value, found, _ := unstructured.NestedFieldNoCopy(object, fields...)
	if !found {
		return 0
	}
	switch number := value.(type) {
	case float64:
		return number
	case float32:
		return float64(number)
	case int64:
		return float64(number)
	case int32:
		return float64(number)
	case int:
		return float64(number)
	case json.Number:
		result, _ := number.Float64()
		return result
	default:
		return 0
	}
}

func buildSchedulerState(nodes []corev1.Node, pods []corev1.Pod) (string, string, []schedulerNodeState, error) {
	requested := map[string]corev1.ResourceList{}
	for i := range pods {
		pod := &pods[i]
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed || pod.Spec.NodeName == "" {
			continue
		}
		resources := requested[pod.Spec.NodeName]
		if resources == nil {
			resources = corev1.ResourceList{}
		}
		for _, container := range pod.Spec.Containers {
			for name, quantity := range container.Resources.Requests {
				current := resources[name]
				current.Add(quantity)
				resources[name] = current
			}
		}
		requested[pod.Spec.NodeName] = resources
	}
	state := make([]schedulerNodeState, 0, len(nodes))
	for i := range nodes {
		node := &nodes[i]
		formatted := map[string]string{}
		for name, quantity := range requested[node.Name] {
			formatted[string(name)] = quantity.String()
		}
		state = append(state, schedulerNodeState{
			NodeName: node.Name, NodeUID: string(node.UID), Ready: nodeReady(node), Unschedulable: node.Spec.Unschedulable,
			RequestedResources: formatted,
		})
	}
	sort.Slice(state, func(i, j int) bool { return state[i].NodeUID < state[j].NodeUID })
	id, err := contentHash(state)
	return id, time.Now().UTC().Format(time.RFC3339), state, err
}

// BuildSchedulerState使用与Reconciler相同的生产逻辑生成一次请求携带的Node动态状态。
func BuildSchedulerState(nodes []corev1.Node, pods []corev1.Pod) (string, string, any, error) {
	id, capturedAt, state, err := buildSchedulerState(nodes, pods)
	return id, capturedAt, state, err
}

func contentHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal snapshot: %w", err)
	}
	// Normalize structs into generic maps before hashing. encoding/json sorts
	// map keys, which makes this identity identical to Algorithm's
	// json.dumps(sort_keys=True, separators=(",", ":")) canonical form.
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return "", fmt.Errorf("normalize snapshot: %w", err)
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("marshal canonical snapshot: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func nodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func directTaskPods(pods []corev1.Pod, namespace string, taskUID types.UID) []corev1.Pod {
	result := []corev1.Pod{}
	for i := range pods {
		pod := &pods[i]
		if pod.Namespace != namespace {
			continue
		}
		for _, owner := range pod.OwnerReferences {
			if owner.UID == taskUID {
				result = append(result, *pod.DeepCopy())
				break
			}
		}
	}
	return result
}
