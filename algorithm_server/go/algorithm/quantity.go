package algorithm

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

const bytesPerMi int64 = 1024 * 1024

// normalizeWorkerResources is the Go boundary for Kubernetes Quantity values.
// The Python worker receives only decimal base-unit strings: millicores for CPU
// and bytes for memory.
func normalizeWorkerResources(request map[string]any, snapshot staticSnapshot) (map[string]any, staticSnapshot, *apiError) {
	normalizedRequest := copyMap(request)
	ngd, ok := request["ngd"].(map[string]any)
	if !ok {
		return nil, staticSnapshot{}, &apiError{RequestID: stringValue(request["requestId"]), Code: "INVALID_REQUEST", Message: "ngd must be an object", Status: 400}
	}
	ngdCopy := copyMap(ngd)
	normalizedSets := map[string]any{}
	for _, field := range []string{"minResources", "quota"} {
		values, err := normalizeResourceMap(ngd[field], "spec."+field)
		if err != nil {
			err.RequestID = stringValue(request["requestId"])
			return nil, staticSnapshot{}, err
		}
		normalizedSets[field] = values
	}
	minimum := normalizedSets["minResources"].(map[string]any)
	quota := normalizedSets["quota"].(map[string]any)
	for name, rawMinimum := range minimum {
		rawLimit, exists := quota[name]
		if !exists {
			continue
		}
		minimumValue, _ := strconv.ParseInt(stringValue(rawMinimum), 10, 64)
		limitValue, _ := strconv.ParseInt(stringValue(rawLimit), 10, 64)
		if minimumValue > limitValue {
			return nil, staticSnapshot{}, &apiError{
				RequestID: stringValue(request["requestId"]),
				Code:      "MIN_RESOURCES_EXCEEDS_QUOTA",
				Message: fmt.Sprintf("spec.minResources.%s=%s exceeds spec.quota.%s=%s",
					name, resourceInputValue(ngd["minResources"], name), name, resourceInputValue(ngd["quota"], name)),
				Status: 422,
			}
		}
	}
	ngdCopy["normalizedResources"] = normalizedSets
	normalizedRequest["ngd"] = ngdCopy

	states, ok := resourceStateMaps(request["nodeUsageStates"])
	if !ok {
		return nil, staticSnapshot{}, &apiError{RequestID: stringValue(request["requestId"]), Code: "INVALID_REQUEST", Message: "nodeUsageStates must be an array", Status: 400}
	}
	normalizedStates := make([]map[string]any, 0, len(states))
	for _, state := range states {
		item := copyMap(state)
		values, err := normalizeResourceMap(state["requestedResources"], "nodeUsageStates.requestedResources")
		if err != nil {
			err.RequestID = stringValue(request["requestId"])
			return nil, staticSnapshot{}, err
		}
		item["normalizedRequestedResources"] = values
		normalizedStates = append(normalizedStates, item)
	}
	normalizedRequest["nodeUsageStates"] = normalizedStates

	normalizedSnapshot := snapshot
	normalizedSnapshot.Nodes = make([]map[string]any, 0, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		item := copyMap(node)
		values, err := normalizeResourceMap(node["allocatable"], fmt.Sprintf("Node %s allocatable", stringValue(node["nodeName"])))
		if err != nil {
			err.RequestID = stringValue(request["requestId"])
			return nil, staticSnapshot{}, err
		}
		item["normalizedAllocatableResources"] = values
		normalizedSnapshot.Nodes = append(normalizedSnapshot.Nodes, item)
	}
	return normalizedRequest, normalizedSnapshot, nil
}

func resourceStateMaps(raw any) ([]map[string]any, bool) {
	switch states := raw.(type) {
	case []map[string]any:
		return states, true
	case []any:
		result := make([]map[string]any, 0, len(states))
		for _, rawState := range states {
			state, ok := rawState.(map[string]any)
			if !ok {
				return nil, false
			}
			result = append(result, state)
		}
		return result, true
	default:
		return nil, false
	}
}

func normalizeResourceMap(raw any, path string) (map[string]any, *apiError) {
	result := map[string]any{}
	if raw == nil {
		return result, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, &apiError{Code: "INVALID_RESOURCE_QUANTITY", Message: path + " must be an object", Status: 400}
	}
	for name, rawValue := range values {
		value := strings.TrimSpace(stringValue(rawValue))
		parsed, err := parseQuantityValue(value, name)
		if err != nil {
			return nil, &apiError{Code: "INVALID_RESOURCE_QUANTITY", Message: fmt.Sprintf("%s.%s=%q is invalid: %v", path, name, value, err), Status: 400}
		}
		if parsed < 0 {
			return nil, &apiError{Code: "INVALID_RESOURCE_QUANTITY", Message: fmt.Sprintf("%s.%s=%q must not be negative", path, name, value), Status: 400}
		}
		result[name] = strconv.FormatInt(parsed, 10)
	}
	return result, nil
}

func parseQuantityValue(raw, name string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	quantity, err := resource.ParseQuantity(raw)
	if err != nil {
		return 0, err
	}
	if name == "cpu" {
		return quantity.MilliValue(), nil
	}
	return quantity.Value(), nil
}

func resourceInputValue(raw any, name string) string {
	values, _ := raw.(map[string]any)
	return stringValue(values[name])
}

func formatCandidateResources(groups []map[string]any) *apiError {
	for _, group := range groups {
		nodes, _ := group["nodes"].([]any)
		for _, rawNode := range nodes {
			node, ok := rawNode.(map[string]any)
			if !ok {
				return &apiError{Code: "ALGORITHM_WORKER_PROTOCOL_ERROR", Message: "candidate group contains a malformed Node", Status: 500, Retryable: true}
			}
			internal, ok := node["resources"].(map[string]any)
			if !ok {
				continue
			}
			cpu, cpuErr := strconv.ParseInt(stringValue(internal["cpuMilli"]), 10, 64)
			memory, memoryErr := strconv.ParseInt(stringValue(internal["memoryBytes"]), 10, 64)
			if cpuErr != nil || memoryErr != nil || cpu < 0 || memory < 0 {
				return &apiError{Code: "ALGORITHM_WORKER_PROTOCOL_ERROR", Message: "candidate Node resources are not normalized non-negative integers", Status: 500, Retryable: true}
			}
			node["resources"] = map[string]any{
				"cpuAvailable":    fmt.Sprintf("%dm", cpu),
				"memoryAvailable": formatMemoryMi(memory),
			}
		}
	}
	return nil
}

func formatMemoryMi(bytes int64) string {
	if bytes <= 0 {
		return "0Mi"
	}
	return fmt.Sprintf("%dMi", bytes/bytesPerMi)
}

func formatWorkerFailure(failure *workerFailure) *allocationFailure {
	if failure == nil || failure.Code == "" {
		return nil
	}
	details := failure.Details
	integer := func(name string) int64 {
		value, _ := strconv.ParseInt(stringValue(details[name]), 10, 64)
		return value
	}
	message := "Algorithm returned no feasible node group"
	switch failure.Code {
	case "NODE_SELECTOR_NO_MATCH":
		message = fmt.Sprintf("nodeSelector matched 0 of %d topology-resolved nodes", integer("inputNodeCount"))
	case "NO_NODES_IN_TOPOLOGY_SCOPE":
		message = fmt.Sprintf("topology constraints matched 0 of %d nodeSelector-matched nodes", integer("selectorMatchedNodeCount"))
	case "ALL_MATCHING_NODES_UNAVAILABLE":
		message = fmt.Sprintf("all %d matching nodes are unavailable or unschedulable", integer("matchingNodeCount"))
	case "NO_TOPOLOGY_GROUP":
		message = fmt.Sprintf("no topology group could be built from %d eligible nodes", integer("eligibleNodeCount"))
	case "QUOTA_PREVENTS_MINIMUM":
		message = fmt.Sprintf("%d nodes matched, but no node group can satisfy minResources within quota %s",
			integer("eligibleNodeCount"), formatResourceSet(details["quota"]))
	case "MAX_NODES_PREVENTS_MINIMUM":
		message = fmt.Sprintf("minResources %s cannot be satisfied within maxNodes=%d; maximum available is %s",
			formatResourceSet(details["required"]), integer("maxNodes"), formatResourceSet(details["achievable"]))
	case "INSUFFICIENT_RESOURCES":
		message = fmt.Sprintf("required %s; maximum available within maxNodes=%d is %s",
			formatResourceSet(details["required"]), integer("maxNodes"), formatResourceSet(details["achievable"]))
	}
	return &allocationFailure{Code: failure.Code, Message: message, Details: details}
}

func formatResourceSet(raw any) string {
	values, _ := raw.(map[string]any)
	parts := make([]string, 0, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, name := range keys {
		value, _ := strconv.ParseInt(stringValue(values[name]), 10, 64)
		if name == "cpu" {
			parts = append(parts, fmt.Sprintf("cpu=%dm", value))
		} else if name == "memory" {
			parts = append(parts, "memory="+formatMemoryMi(value))
		} else {
			parts = append(parts, fmt.Sprintf("%s=%d", name, value))
		}
	}
	return strings.Join(parts, ",")
}
