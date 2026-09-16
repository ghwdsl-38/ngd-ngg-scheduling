package common

import (
	"encoding/json"
	"fmt"
	"strings"
)

var removedKeys = map[string]struct{}{
	"resourceVersion": {}, "creationTimestamp": {}, "managedFields": {},
	"ownerReferences": {},
	"timestamp":       {}, "lastUpdated": {}, "capturedAt": {}, "capturedAtUnix": {},
	"metricSnapshotCapturedAt": {}, "timing": {}, "durationMs": {},
}

// NormalizeForGolden删除非确定运行时字段，同时保持候选组和Node数组原顺序。
func NormalizeForGolden(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		nodeName := fmt.Sprint(typed["nodeName"])
		for key, item := range typed {
			if _, remove := removedKeys[key]; remove {
				continue
			}
			switch {
			case key == "nodeUsageStates":
				if _, alreadySummary := item.(map[string]any); alreadySummary {
					result[key] = NormalizeForGolden(item)
				} else {
					result[key] = summarizeUsageStates(item)
				}
			case key == "uid":
				result[key] = "<uid>"
			case strings.HasSuffix(key, "/demand-uid"):
				result[key] = "<demand-uid-hash>"
			case key == "algorithmBootId" || key == "bootId":
				result[key] = "<boot-id>"
			case key == "requestId":
				result[key] = "<request-id>"
			case key == "taskUID" || key == "ngdUID":
				result[key] = "<uid>"
			case key == "nodeUID" && nodeName != "" && nodeName != "<nil>":
				result[key] = "uid:" + nodeName
			case strings.HasSuffix(strings.ToLower(key), "snapshotid"):
				result[key] = "<snapshot-hash>"
			default:
				result[key] = NormalizeForGolden(item)
			}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index := range typed {
			result[index] = NormalizeForGolden(typed[index])
		}
		return result
	default:
		return typed
	}
}

func summarizeUsageStates(value any) map[string]any {
	items, _ := value.([]any)
	inUseCount := 0
	withRequests := 0
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if inUse, _ := item["inUse"].(bool); inUse {
			inUseCount++
		}
		if resources, ok := item["requestedResources"].(map[string]any); ok && len(resources) > 0 {
			withRequests++
		}
	}
	return map[string]any{"nodeCount": len(items), "inUseCount": inUseCount, "nodesWithRequestedResources": withRequests}
}

// ToGeneric把任意Go结构转换为保留json.Number的通用JSON对象。
func ToGeneric(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var generic any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&generic); err != nil {
		return nil, err
	}
	return generic, nil
}
