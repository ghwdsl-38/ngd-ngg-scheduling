package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	SwitchID      string         `json:"switchId,omitempty"`
	LeafSwitchID  string         `json:"leafSwitchId,omitempty"`
	LeafSwitchIDs []string       `json:"leafSwitchIds"`
	Links         []topologyLink `json:"links,omitempty"`
}

type topologyLink struct {
	BondName     string `json:"bond,omitempty"`
	BondMode     string `json:"bondMode,omitempty"`
	Interface    string `json:"interface"`
	LeafSwitchID string `json:"leafSwitchId"`
	RemotePortID string `json:"remotePortId,omitempty"`
	Active       bool   `json:"active"`
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

func buildStaticSnapshot(clusterID string, nodes []corev1.Node) (string, staticSnapshot, error) {
	result := staticSnapshot{ClusterID: clusterID, TopologyVersion: "node-leaf-set-v2"}
	for i := range nodes {
		node := &nodes[i]
		topo, found, topologyErr := topologyFromNode(node)
		if topologyErr != nil {
			return "", result, fmt.Errorf("Node %s topology: %w", node.Name, topologyErr)
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
	if len(result.Nodes) == 0 {
		return "", result, fmt.Errorf("no Worker has usable Node Leaf labels or annotations")
	}
	id, err := contentHash(result)
	return id, result, err
}

// BuildStaticSnapshot使用与Reconciler相同的生产逻辑生成内容Hash和可序列化快照。
func BuildStaticSnapshot(clusterID string, nodes []corev1.Node) (string, any, error) {
	id, snapshot, err := buildStaticSnapshot(clusterID, nodes)
	return id, snapshot, err
}

func topologyFromNode(node *corev1.Node) (topology, bool, error) {
	const prefix = "topology.demo.ngg.io/"
	leaves := []string{}
	if raw := node.Annotations[prefix+"leaf-switch-ids"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &leaves); err != nil {
			return topology{}, false, fmt.Errorf("decode leaf-switch-ids: %w", err)
		}
	}
	links := []topologyLink{}
	if raw := node.Annotations[prefix+"leaf-links"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &links); err != nil {
			return topology{}, false, fmt.Errorf("decode leaf-links: %w", err)
		}
		for _, link := range links {
			leaves = append(leaves, link.LeafSwitchID)
		}
	}
	leaf := node.Labels[prefix+"leaf-switch"]
	if leaf == "" {
		leaf = node.Labels[prefix+"switch"]
	}
	if len(leaves) == 0 && leaf != "" {
		leaves = append(leaves, leaf)
	}
	leaves = canonicalStrings(leaves)
	if len(leaves) == 0 {
		return topology{}, false, nil
	}
	if len(leaves) > 2 {
		return topology{}, false, fmt.Errorf("resolved %d Leaf switches; maximum is 2", len(leaves))
	}
	for i := range links {
		links[i].Interface = strings.TrimSpace(links[i].Interface)
		links[i].LeafSwitchID = strings.TrimSpace(links[i].LeafSwitchID)
		if links[i].Interface == "" || links[i].LeafSwitchID == "" {
			return topology{}, false, fmt.Errorf("every leaf-links entry needs interface and leafSwitchId")
		}
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].Interface == links[j].Interface {
			return links[i].LeafSwitchID < links[j].LeafSwitchID
		}
		return links[i].Interface < links[j].Interface
	})
	return topology{SwitchID: leaves[0], LeafSwitchID: leaves[0], LeafSwitchIDs: leaves, Links: links}, true, nil
}

func canonicalStrings(values []string) []string {
	set := map[string]struct{}{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
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
