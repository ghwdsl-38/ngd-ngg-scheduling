// cache.go 实现 Node 静态快照的校验、内容 Hash 和双版本内存缓存。
package algorithm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type staticCache struct {
	// 读写锁允许 HTTP 状态查询与调度计算并发读取快照。
	mu       sync.RWMutex
	current  *staticSnapshot
	previous *staticSnapshot
}

func (c *staticCache) put(id string, body map[string]any) (staticSnapshot, error) {
	// 路径中的 snapshotID 必须与规范化内容的 SHA-256 完全一致。
	if !strings.HasPrefix(id, "sha256:") || len(id) != 71 {
		return staticSnapshot{}, fmt.Errorf("snapshotId must use sha256:<hex> format")
	}
	content := map[string]any{
		"clusterId":       stringValue(body["clusterId"]),
		"topologyVersion": stringValue(body["topologyVersion"]),
		"nodes":           body["nodes"],
	}
	actual, err := canonicalHash(content)
	if err != nil {
		return staticSnapshot{}, err
	}
	if actual != id {
		return staticSnapshot{}, fmt.Errorf("snapshot checksum mismatch: path=%s actual=%s", id, actual)
	}
	rawNodes, ok := body["nodes"].([]any)
	if !ok || len(rawNodes) == 0 {
		return staticSnapshot{}, fmt.Errorf("static snapshot nodes must be a non-empty array")
	}
	nodes := make([]map[string]any, 0, len(rawNodes))
	seen := map[string]struct{}{}
	// Node UID 是节点身份；同名重建节点不会误用旧授权。
	for _, raw := range rawNodes {
		node, ok := raw.(map[string]any)
		if !ok {
			return staticSnapshot{}, fmt.Errorf("static snapshot contains a malformed Node")
		}
		name, uid := stringValue(node["nodeName"]), stringValue(node["nodeUID"])
		topology, ok := node["topology"].(map[string]any)
		if !ok {
			return staticSnapshot{}, fmt.Errorf("Node %q has malformed topology", name)
		}
		leaves, err := staticNodeLeafIDs(topology)
		if err != nil {
			return staticSnapshot{}, fmt.Errorf("Node %q: %w", name, err)
		}
		if name == "" || uid == "" || len(leaves) == 0 {
			return staticSnapshot{}, fmt.Errorf("every Node needs nodeName, nodeUID and at least one Leaf")
		}
		topology["leafSwitchIds"] = leaves
		topology["leafSwitchId"] = leaves[0]
		topology["switchId"] = leaves[0]
		if _, found := seen[uid]; found {
			return staticSnapshot{}, fmt.Errorf("duplicate nodeUID %s", uid)
		}
		seen[uid] = struct{}{}
		nodes = append(nodes, node)
	}
	// 固定排序保证同一份逻辑数据在后续计算中始终具有稳定顺序。
	sort.Slice(nodes, func(i, j int) bool {
		left, right := stringValue(nodes[i]["nodeUID"]), stringValue(nodes[j]["nodeUID"])
		if left == right {
			return stringValue(nodes[i]["nodeName"]) < stringValue(nodes[j]["nodeName"])
		}
		return left < right
	})
	snapshot := staticSnapshot{
		SnapshotID: id, ClusterID: stringValue(body["clusterId"]),
		TopologyVersion: stringValue(body["topologyVersion"]), Nodes: nodes,
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current != nil && c.current.SnapshotID == id {
		return *c.current, nil
	}
	// 仅保留 current 和 previous，支持 PRC 切换快照期间的短暂并发请求。
	if c.current != nil {
		copy := *c.current
		c.previous = &copy
	}
	c.current = &snapshot
	return snapshot, nil
}

func staticNodeLeafIDs(topology map[string]any) ([]string, error) {
	values := []string{}
	switch raw := topology["leafSwitchIds"].(type) {
	case []any:
		for _, value := range raw {
			values = append(values, stringValue(value))
		}
	case []string:
		values = append(values, raw...)
	case nil:
	default:
		return nil, fmt.Errorf("leafSwitchIds must be an array")
	}
	if len(values) == 0 {
		leaf := stringValue(topology["leafSwitchId"])
		if leaf == "" {
			leaf = stringValue(topology["switchId"])
		}
		values = append(values, leaf)
	}
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
	if len(result) == 0 {
		return nil, fmt.Errorf("leafSwitchIds cannot be empty")
	}
	if len(result) > 2 {
		return nil, fmt.Errorf("leafSwitchIds contains %d Leaves; maximum is 2", len(result))
	}
	return result, nil
}

func (c *staticCache) get(id string) (staticSnapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, item := range []*staticSnapshot{c.current, c.previous} {
		if item != nil && item.SnapshotID == id {
			return *item, true
		}
	}
	return staticSnapshot{}, false
}

func (c *staticCache) status() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := map[string]any{"ready": false, "currentSnapshotId": "", "previousSnapshotId": "", "nodeCount": 0}
	if c.current != nil {
		result["ready"] = true
		result["currentSnapshotId"] = c.current.SnapshotID
		result["nodeCount"] = len(c.current.Nodes)
	}
	if c.previous != nil {
		result["previousSnapshotId"] = c.previous.SnapshotID
	}
	return result
}

func canonicalHash(value any) (string, error) {
	// Go JSON 编码会稳定排序 map key；关闭 HTML 转义以和协议 Hash 规则一致。
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", fmt.Errorf("marshal canonical value: %w", err)
	}
	raw := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
