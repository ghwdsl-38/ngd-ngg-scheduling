package group3_prc_ngd_ngg_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"demo.ngg/go-test-suites/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	prcapp "scheduling.demo.ngg.io/prc/pkg/application"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	formalDemandGVK = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupDemand"}
	formalGrantGVK  = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupGrant"}
)

func TestGroup3_PRCWatchesNGDAndCreatesNGG(t *testing.T) {
	groupDirectory := directory(t)
	fixture, err := common.GenerateFixture(1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := common.WriteFixtureInput(filepath.Join(groupDirectory, "testdata", "input"), fixture, true); err != nil {
		t.Fatal(err)
	}
	runDirectory := common.NewRunDirectory(t, groupDirectory)
	environment := common.StartEnvTest(t)
	defer environment.Stop(t)
	apiClient, err := client.New(environment.Config, client.Options{Scheme: environment.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	setupContext, setupCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer setupCancel()
	if err := common.CreateKubernetesInputs(setupContext, apiClient, fixture); err != nil {
		t.Fatal(err)
	}

	mock := newMockAlgorithm(t)
	defer mock.server.Close()
	reconcileStarted := make(chan time.Time, 1)
	var reconcileStartOnce sync.Once
	prcApplication, err := prcapp.New(prcapp.Config{
		KubernetesConfig:       environment.Config,
		Scheme:                 environment.Scheme,
		AlgorithmURL:           mock.server.URL,
		ClusterID:              "mock-1000-node-cluster",
		HTTPClient:             mock.server.Client(),
		MetricsBindAddress:     "0",
		HealthProbeBindAddress: "0",
		DebugAlgorithmTrace:    true,
		ReconcileObserver: func(_ string, _ int64, observedAt time.Time) {
			reconcileStartOnce.Do(func() { reconcileStarted <- observedAt })
		},
	})
	if err != nil {
		t.Fatalf("create PRC application: %v", err)
	}
	managerContext, managerCancel := context.WithCancel(context.Background())
	managerErrors := make(chan error, 1)
	go func() { managerErrors <- prcApplication.Start(managerContext) }()
	defer func() {
		managerCancel()
		select {
		case err := <-managerErrors:
			if err != nil {
				t.Errorf("manager stopped: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("manager did not stop")
		}
	}()
	initialStatic, err := prcApplication.WaitForReady(setupContext)
	if err != nil {
		t.Fatalf("static snapshot did not become ready: %v", err)
	}
	// 没有创建任何NGD时修改Node静态Label，验证独立Controller会主动生成新Hash并上传。
	changedNode := &corev1.Node{}
	if err := apiClient.Get(setupContext, client.ObjectKey{Name: fixture.Nodes[0].Name}, changedNode); err != nil {
		t.Fatalf("get Node for independent static sync: %v", err)
	}
	changedNode.Labels["tests.ngg.io/static-revision"] = "2"
	if err := apiClient.Update(setupContext, changedNode); err != nil {
		t.Fatalf("update Node static Label: %v", err)
	}
	if err := common.Eventually(setupContext, 10*time.Millisecond, func(context.Context) (bool, error) {
		status, ready := prcApplication.StaticSnapshotStatus()
		return ready && status.SnapshotID != initialStatic.SnapshotID, nil
	}); err != nil {
		t.Fatalf("wait independent static snapshot refresh: %v", err)
	}
	putCountBeforeDemand := mock.puts()

	demand := fixture.Demand.DeepCopy()
	demand.SetUID("")
	demand.SetResourceVersion("")
	demand.SetCreationTimestamp(metav1.Time{})
	demand.SetGroupVersionKind(formalDemandGVK)
	if err := apiClient.Create(setupContext, demand); err != nil {
		t.Fatalf("create formal NGD: %v", err)
	}
	waitContext, waitCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer waitCancel()
	var started time.Time
	select {
	case started = <-reconcileStarted:
	case <-waitContext.Done():
		t.Fatalf("wait PRC to observe NGD: %v", waitContext.Err())
	}
	var grant, finalDemand *unstructured.Unstructured
	err = common.Eventually(waitContext, 10*time.Millisecond, func(ctx context.Context) (bool, error) {
		grant = &unstructured.Unstructured{}
		grant.SetGroupVersionKind(formalGrantGVK)
		if err := apiClient.Get(ctx, client.ObjectKey{Name: "ngg-go-test-demand"}, grant); err != nil {
			return false, err
		}
		grantPhase, _, _ := unstructured.NestedString(grant.Object, "status", "phase")
		return grantPhase == "Active", nil
	})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("wait NGG Active: %v", err)
	}
	// NGD Status仍属于正确性断言，但不纳入“Watch到NGG生成”的业务耗时。
	err = common.Eventually(waitContext, 10*time.Millisecond, func(ctx context.Context) (bool, error) {
		finalDemand = &unstructured.Unstructured{}
		finalDemand.SetGroupVersionKind(formalDemandGVK)
		if err := apiClient.Get(ctx, client.ObjectKey{Name: "go-test-demand"}, finalDemand); err != nil {
			return false, err
		}
		demandPhase, _, _ := unstructured.NestedString(finalDemand.Object, "status", "phase")
		return demandPhase == "Fulfilled", nil
	})
	if err != nil {
		t.Fatalf("wait NGD Fulfilled: %v", err)
	}
	common.WriteTiming(t, runDirectory, "PRC observes NGD -> NGG Active", elapsed)

	request, response := mock.snapshot()
	if request == nil || response == nil {
		t.Fatal("PRC did not call Mock Algorithm")
	}
	nodes, _, _ := unstructured.NestedSlice(grant.Object, "spec", "nodes")
	if len(nodes) != 3 {
		t.Fatalf("formal NGG nodes=%d, want 3", len(nodes))
	}
	if _, found, _ := unstructured.NestedSlice(grant.Object, "spec", "candidateNodeGroups"); found {
		t.Fatal("formal NGG must contain one nodes list, not candidateNodeGroups")
	}
	if got := mock.puts(); got != putCountBeforeDemand {
		t.Fatalf("NGD Reconcile issued static PUTs: before=%d after=%d", putCountBeforeDemand, got)
	}

	actualDirectory := filepath.Join(runDirectory, "actual")
	_ = common.WriteYAML(filepath.Join(actualDirectory, "ngd-input.yaml"), demand.Object)
	_ = common.WriteJSON(filepath.Join(actualDirectory, "prc-allocation-request.json"), request)
	_ = common.WriteJSON(filepath.Join(actualDirectory, "mock-algorithm-response.json"), response)
	_ = common.WriteYAML(filepath.Join(actualDirectory, "ngg-raw.yaml"), grant.Object)
	_ = common.WriteYAML(filepath.Join(actualDirectory, "ngd-status.yaml"), map[string]any{"status": finalDemand.Object["status"]})
	comparison := map[string]any{"ngg": grant.Object, "ngdStatus": finalDemand.Object["status"]}
	common.CompareGolden(t, filepath.Join(groupDirectory, "testdata", "expected", "ngg-and-status.json"), filepath.Join(actualDirectory, "ngg-normalized.json"), filepath.Join(runDirectory, "comparison", "diff.txt"), comparison)

}

type mockAlgorithm struct {
	server   *httptest.Server
	mu       sync.Mutex
	staticID string
	static   map[string]any
	putCount int
	request  map[string]any
	response map[string]any
	bootID   string
}

func newMockAlgorithm(t *testing.T) *mockAlgorithm {
	t.Helper()
	mock := &mockAlgorithm{bootID: "mock-algorithm-group3"}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.handle))
	return mock
}

func (m *mockAlgorithm) handle(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(request.Body).Decode(&body)
	writer.Header().Set("Content-Type", "application/json")
	if request.Method == http.MethodGet && request.URL.Path == "/internal/v1/node-static-cache/status" {
		m.mu.Lock()
		id, count := m.staticID, 0
		if nodes, ok := m.static["nodes"].([]any); ok {
			count = len(nodes)
		}
		m.mu.Unlock()
		_ = json.NewEncoder(writer).Encode(map[string]any{"algorithmBootId": m.bootID, "ready": id != "", "acceptedSnapshotId": id, "nodeCount": count})
		return
	}
	if request.Method == http.MethodPut && strings.HasPrefix(request.URL.Path, "/internal/v1/node-static-snapshots/") {
		id := strings.TrimPrefix(request.URL.Path, "/internal/v1/node-static-snapshots/")
		m.mu.Lock()
		m.staticID, m.static = id, body
		m.putCount++
		m.mu.Unlock()
		_ = json.NewEncoder(writer).Encode(map[string]any{"algorithmBootId": m.bootID, "acceptedSnapshotId": id, "nodeCount": len(body["nodes"].([]any))})
		return
	}
	if request.Method != http.MethodPost || request.URL.Path != "/api/v1/allocate" {
		http.NotFound(writer, request)
		return
	}
	states, _ := body["schedulerState"].([]any)
	available := make([]map[string]any, 0, len(states))
	for _, raw := range states {
		state := raw.(map[string]any)
		ready, _ := state["ready"].(bool)
		unschedulable, _ := state["unschedulable"].(bool)
		if ready && !unschedulable {
			available = append(available, state)
		}
	}
	sort.Slice(available, func(i, j int) bool {
		return fmt.Sprint(available[i]["nodeName"]) < fmt.Sprint(available[j]["nodeName"])
	})
	candidates := make([]any, 0, 3)
	for index, state := range available[:3] {
		candidates = append(candidates, map[string]any{
			"nodeUID": state["nodeUID"], "nodeName": state["nodeName"], "score": int64(90 - index*5),
			"resources": map[string]any{"cpuAvailable": "32", "memoryAvailable": "128Gi"},
			"topology": map[string]any{
				"regionId": "CN-NORTH", "locationId": "HB-HL", "dataCenterId": "HB-HL-DC1",
				"roomId": "HB-HL-DC1-102", "borderDomainId": "HB-HL-DC1-102-BORDER-DOMAIN-01", "leafSwitchId": "leaf-001",
			},
		})
	}
	response := map[string]any{
		"requestId": body["requestId"], "taskUID": body["taskUID"], "ngdUID": body["ngdUID"], "ngdGeneration": body["ngdGeneration"],
		"algorithmBootId": m.bootID, "nodeStaticSnapshotId": body["nodeStaticSnapshotId"], "schedulerStateSnapshotId": body["schedulerStateSnapshotId"],
		"metricSnapshotId": "mock-metrics-group3", "metricSnapshotCapturedAt": "2026-08-21T00:00:00Z", "topologySnapshotId": "mock-unicom-topology", "degraded": false, "warnings": []any{}, "status": "SUCCESS",
		"candidateNodeGroups": []any{map[string]any{"rank": int64(1), "groupId": "leaf:leaf-001", "topologyLevel": "leafSwitch", "groupScore": 88.5, "nodes": candidates}},
	}
	m.mu.Lock()
	m.request = body
	m.response = response
	m.mu.Unlock()
	_ = json.NewEncoder(writer).Encode(response)
}

func (m *mockAlgorithm) snapshot() (map[string]any, map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.request, m.response
}

func (m *mockAlgorithm) puts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.putCount
}

func directory(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve group directory")
	}
	return filepath.Dir(source)
}
