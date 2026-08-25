// service.go 编排一次任务级计算：解析快照、规范化动态状态、调用 Worker 并组装响应。
package algorithm

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type service struct {
	bootID  string
	static  *staticCache
	metrics *metricsCache
	worker  calculator
}

func (s *service) allocate(ctx context.Context, request map[string]any, legacy bool) (map[string]any, *apiError) {
	// 请求必须引用已经由 PRC PUT 到本进程中的静态快照。
	requestID := stringValue(request["requestId"])
	for _, field := range []string{"requestId", "taskUID", "ngdUID", "nodeStaticSnapshotId"} {
		if stringValue(request[field]) == "" {
			return nil, &apiError{RequestID: requestID, Code: "INVALID_REQUEST", Message: field + " must not be empty", Status: 400}
		}
	}
	static, found := s.static.get(stringValue(request["nodeStaticSnapshotId"]))
	if !found {
		return nil, &apiError{RequestID: requestID, Code: "STATIC_SNAPSHOT_NOT_FOUND", Message: fmt.Sprintf("node static snapshot %q is not available", request["nodeStaticSnapshotId"]), Retryable: true, Status: 409}
	}
	// 新协议直接发送 nodeUsageStates；旧协议 schedulerState 在 Go 中适配。
	normalized, err := normalizeUsageStates(request, static.Nodes)
	if err != nil {
		return nil, &apiError{RequestID: requestID, Code: "INVALID_REQUEST", Message: err.Error(), Status: 400}
	}
	requestCopy := copyMap(request)
	requestCopy["nodeUsageStates"] = normalized
	metric, degraded, warnings := s.metrics.resolve()
	warnings = append(warnings, ignoredNGDWarnings(requestCopy)...)
	// Worker 每次收到完整上下文，因此 Python 不需要维护跨请求缓存。
	result, workerErr := s.worker.calculate(ctx, workerPayload{Request: requestCopy, StaticSnapshot: static, MetricSnapshot: metric, MetricsDegraded: degraded, Warnings: warnings})
	if workerErr != nil {
		return nil, workerErr
	}
	groups := result.CandidateNodeGroups
	if legacy {
		for _, group := range groups {
			if stringValue(group["topologyLevel"]) == "leafSwitch" {
				group["topologyLevel"] = "leafGroup"
			}
			id := stringValue(group["groupId"])
			id = strings.TrimPrefix(id, "leaf:")
			id = strings.TrimPrefix(id, "border:")
			group["groupId"] = strings.TrimPrefix(id, "core:")
		}
	}
	metricID := "metrics-disabled"
	metricCapturedAt := ""
	if s.metrics.enabled() {
		metricID = "metrics-unavailable"
	}
	if metric != nil {
		metricID = metric.SnapshotID
		metricCapturedAt = metric.CapturedAt
	}
	status := "SUCCESS"
	if len(groups) == 0 {
		status = "UNSATISFIABLE"
	}
	response := map[string]any{
		"requestId": requestID, "taskUID": stringValue(request["taskUID"]), "ngdUID": stringValue(request["ngdUID"]),
		"ngdGeneration": request["ngdGeneration"], "algorithmBootId": s.bootID,
		"nodeStaticSnapshotId": static.SnapshotID, "metricsSnapshotId": metricID, "metricSnapshotId": metricID,
		"metricSnapshotCapturedAt": metricCapturedAt,
		"degraded":                 degraded, "warnings": warnings, "status": status, "candidateNodeGroups": groups,
	}
	if request["debugTrace"] == true && len(result.PipelineTrace) > 0 {
		response["pipelineTrace"] = result.PipelineTrace
	}
	if value, ok := request["schedulerStateSnapshotId"]; ok {
		response["schedulerStateSnapshotId"] = stringValue(value)
	}
	if len(groups) == 0 {
		response["reason"] = "NO_FEASIBLE_NODE_GROUP"
	}
	return response, nil
}

func normalizeUsageStates(request map[string]any, nodes []map[string]any) ([]map[string]any, error) {
	// 所有动态状态必须引用静态快照中已知的 Node UID。
	known := map[string]map[string]any{}
	for _, node := range nodes {
		known[stringValue(node["nodeUID"])] = node
	}
	// 优先采用新协议的完整布尔状态，不接受增量或重复 UID。
	if raw, exists := request["nodeUsageStates"]; exists {
		items, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("nodeUsageStates must be an array")
		}
		seen := map[string]struct{}{}
		result := make([]map[string]any, 0, len(items))
		for _, rawItem := range items {
			item, ok := rawItem.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("nodeUsageStates contains a malformed entry")
			}
			uid := stringValue(item["nodeUID"])
			if _, ok := known[uid]; !ok {
				return nil, fmt.Errorf("nodeUsageStates contains unknown Node UID %q", uid)
			}
			if _, ok := seen[uid]; ok {
				return nil, fmt.Errorf("duplicate nodeUsageStates entry for %s", uid)
			}
			inUse, ok := item["inUse"].(bool)
			if !ok {
				return nil, fmt.Errorf("nodeUsageStates[%s].inUse must be boolean", uid)
			}
			seen[uid] = struct{}{}
			normalized := map[string]any{"nodeUID": uid, "inUse": inUse}
			if resources, ok := item["requestedResources"].(map[string]any); ok {
				normalized["requestedResources"] = resources
			}
			result = append(result, normalized)
		}
		return result, nil
	}
	// 兼容当前 PRC schedulerState：NotReady、不可调度或资源占满均视为 inUse。
	stateByUID := map[string]map[string]any{}
	if raw, ok := request["schedulerState"].([]any); ok {
		for _, rawItem := range raw {
			if item, ok := rawItem.(map[string]any); ok {
				stateByUID[stringValue(item["nodeUID"])] = item
			}
		}
	}
	result := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		uid := stringValue(node["nodeUID"])
		state := stateByUID[uid]
		inUse := true
		if state != nil {
			ready, _ := state["ready"].(bool)
			unschedulable, _ := state["unschedulable"].(bool)
			inUse = !ready || unschedulable || resourcesFull(node["allocatable"], state["requestedResources"])
		}
		normalized := map[string]any{"nodeUID": uid, "inUse": inUse}
		if state != nil {
			if resources, ok := state["requestedResources"].(map[string]any); ok {
				normalized["requestedResources"] = resources
			}
		}
		result = append(result, normalized)
	}
	return result, nil
}

func ignoredNGDWarnings(request map[string]any) []string {
	if stringValue(request["requestMode"]) != "resourcePool" {
		return nil
	}
	ngd, ok := request["ngd"].(map[string]any)
	if !ok {
		return nil
	}
	fields := []string{"algorithms", "minThroughput", "crossClusterAffinity", "intraClusterAffinity", "networkReachability", "preferredSubnet"}
	warnings := []string{}
	for _, field := range fields {
		value, exists := ngd[field]
		if !exists || value == nil || stringValue(value) == "" {
			if object, ok := value.(map[string]any); !ok || len(object) == 0 {
				if items, ok := value.([]any); !ok || len(items) == 0 {
					continue
				}
			}
		}
		warnings = append(warnings, "spec."+field+" is preserved but not evaluated by the current algorithm")
	}
	return warnings
}

func resourcesFull(allocatable, requested any) bool {
	// 只有所有声明的正容量资源都达到上限时才认为该节点整体占满。
	capacity, ok1 := allocatable.(map[string]any)
	used, ok2 := requested.(map[string]any)
	if !ok1 || !ok2 || len(capacity) == 0 {
		return false
	}
	hasPositive := false
	for name, raw := range capacity {
		limit := quantity(stringValue(raw), name)
		if limit <= 0 {
			continue
		}
		hasPositive = true
		if quantity(stringValue(used[name]), name) < limit {
			return false
		}
	}
	return hasPositive
}

func quantity(raw, resource string) float64 {
	if raw == "" {
		return 0
	}
	if resource == "cpu" {
		if strings.HasSuffix(raw, "m") {
			value, _ := strconv.ParseFloat(strings.TrimSuffix(raw, "m"), 64)
			return value
		}
		value, _ := strconv.ParseFloat(raw, 64)
		return value * 1000
	}
	multipliers := map[string]float64{"Ki": math.Pow(1024, 1), "Mi": math.Pow(1024, 2), "Gi": math.Pow(1024, 3), "Ti": math.Pow(1024, 4)}
	for suffix, multiplier := range multipliers {
		if strings.HasSuffix(raw, suffix) {
			value, _ := strconv.ParseFloat(strings.TrimSuffix(raw, suffix), 64)
			return value * multiplier
		}
	}
	value, _ := strconv.ParseFloat(raw, 64)
	return value
}

func copyMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
