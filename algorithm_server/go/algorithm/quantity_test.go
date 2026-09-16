package algorithm

import "testing"

func TestNormalizeResourceMapUsesKubernetesQuantity(t *testing.T) {
	values, apiErr := normalizeResourceMap(map[string]any{
		"cpu": "1500m", "memory": "1G",
	}, "spec.minResources")
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	if got := stringValue(values["cpu"]); got != "1500" {
		t.Fatalf("cpu=%s, want 1500 millicores", got)
	}
	if got := stringValue(values["memory"]); got != "1000000000" {
		t.Fatalf("memory=%s, want decimal 1G", got)
	}

	values, apiErr = normalizeResourceMap(map[string]any{"memory": "1Gi"}, "spec.minResources")
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	if got := stringValue(values["memory"]); got != "1073741824" {
		t.Fatalf("memory=%s, want binary 1Gi", got)
	}
}

func TestNormalizeWorkerResourcesRejectsMinimumAboveQuota(t *testing.T) {
	request := map[string]any{
		"requestId": "request-1",
		"ngd": map[string]any{
			"minResources": map[string]any{"memory": "500Gi"},
			"quota":        map[string]any{"memory": "400Gi"},
		},
		"nodeUsageStates": []map[string]any{},
	}
	_, _, apiErr := normalizeWorkerResources(request, staticSnapshot{})
	if apiErr == nil || apiErr.Code != "MIN_RESOURCES_EXCEEDS_QUOTA" {
		t.Fatalf("error=%v, want MIN_RESOURCES_EXCEEDS_QUOTA", apiErr)
	}
	if apiErr.Message != "spec.minResources.memory=500Gi exceeds spec.quota.memory=400Gi" {
		t.Fatalf("message=%q", apiErr.Message)
	}
}

func TestFormatCandidateResourcesUsesStableUnits(t *testing.T) {
	groups := []map[string]any{{"nodes": []any{map[string]any{
		"resources": map[string]any{"cpuMilli": "101830", "memoryBytes": "480657050026"},
	}}}}
	if apiErr := formatCandidateResources(groups); apiErr != nil {
		t.Fatal(apiErr)
	}
	resources := groups[0]["nodes"].([]any)[0].(map[string]any)["resources"].(map[string]any)
	if resources["cpuAvailable"] != "101830m" || resources["memoryAvailable"] != "458390Mi" {
		t.Fatalf("resources=%v", resources)
	}
}

func TestFormatWorkerFailureExplainsQuotaRootCause(t *testing.T) {
	failure := formatWorkerFailure(&workerFailure{
		Code: "QUOTA_PREVENTS_MINIMUM",
		Details: map[string]any{
			"eligibleNodeCount": "3",
			"quota": map[string]any{
				"cpu": "80000", "memory": "429496729600",
			},
		},
	})
	if failure == nil || failure.Code != "QUOTA_PREVENTS_MINIMUM" {
		t.Fatalf("failure=%+v", failure)
	}
	want := "3 nodes matched, but no node group can satisfy minResources within quota cpu=80000m,memory=409600Mi"
	if failure.Message != want {
		t.Fatalf("message=%q, want %q", failure.Message, want)
	}
}
