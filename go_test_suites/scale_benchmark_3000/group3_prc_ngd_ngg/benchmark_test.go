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

	base "demo.ngg/go-test-suites/common"
	scale "demo.ngg/go-test-suites/scale_benchmark_3000/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"scheduling.demo.ngg.io/prc/pkg/controller"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

const groupName = "Group3-PRC-MockAlgorithm-NGG"

var (
	demandGVK = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupDemand"}
	grantGVK  = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupGrant"}
)

func TestGroup3Scale3000(t *testing.T) {
	fixture, err := scale.GenerateFixture()
	if err != nil {
		t.Fatal(err)
	}
	if err := scale.WriteCanonicalInputs(scale.ScaleRoot(), fixture); err != nil {
		t.Fatal(err)
	}
	targets, samples, warmups := benchmarkParameters(t)
	runDirectory := scale.NewRunDirectory(t, currentDirectory(t))
	environment := base.StartEnvTest(t)
	defer environment.Stop(t)
	observerConfig := rest.CopyConfig(environment.Config)
	observerConfig.QPS, observerConfig.Burst = 500, 1000
	apiClient, err := client.New(observerConfig, client.Options{Scheme: environment.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	setupContext, setupCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer setupCancel()
	kubernetesFixture := &base.Fixture{Nodes: fixture.Nodes}
	if err := base.CreateKubernetesInputs(setupContext, apiClient, kubernetesFixture); err != nil {
		t.Fatal(err)
	}

	mock := newScaleMockAlgorithm()
	defer mock.server.Close()
	observed := make(chan time.Time, 1)
	manager, err := ctrl.NewManager(environment.Config, ctrl.Options{Scheme: environment.Scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", LeaderElection: false})
	if err != nil {
		t.Fatal(err)
	}
	staticSnapshots := controller.NewStaticSnapshotState()
	staticReconciler := &controller.NodeStaticSnapshotReconciler{
		Client: manager.GetClient(), AlgorithmURL: mock.server.URL, ClusterID: scale.ClusterID,
		HTTPClient: mock.server.Client(), State: staticSnapshots,
	}
	if err := staticReconciler.SetupWithManager(manager); err != nil {
		t.Fatal(err)
	}
	processor := &controller.DemandProcessor{
		Client: manager.GetClient(), AlgorithmURL: mock.server.URL,
		HTTPClient: mock.server.Client(), StaticSnapshots: staticSnapshots, DebugAlgorithmTrace: false,
		ReconcileObserver: func(_ string, _ int64, at time.Time) {
			select {
			case observed <- at:
			default:
			}
		},
	}
	scheduler := controller.NewRefreshScheduler(0)
	if err := manager.Add(scheduler); err != nil {
		t.Fatal(err)
	}
	reconciler := &controller.NodeGroupDemandReconciler{Client: manager.GetClient(), Scheduler: scheduler, Processor: processor}
	if err := reconciler.SetupWithManager(manager); err != nil {
		t.Fatal(err)
	}
	if err := (&controller.RefreshReconciler{Processor: processor, Scheduler: scheduler, RefreshInterval: time.Hour}).SetupWithManager(manager); err != nil {
		t.Fatal(err)
	}
	managerContext, managerCancel := context.WithCancel(context.Background())
	managerErrors := make(chan error, 1)
	go func() { managerErrors <- manager.Start(managerContext) }()
	defer stopManager(t, managerCancel, managerErrors)
	if !manager.GetCache().WaitForCacheSync(setupContext) {
		t.Fatal("PRC cache did not sync")
	}
	if _, err := staticSnapshots.WaitForReady(setupContext); err != nil {
		t.Fatalf("static snapshot did not become ready: %v", err)
	}

	boundary := "PRC observes NGD with static snapshot pre-synced -> dynamic state/Mock Algorithm -> NGG Active"
	byTarget := map[int][]scale.Sample{}
	statistics := []scale.Statistics{}
	for _, target := range targets {
		for iteration := 1; iteration <= warmups; iteration++ {
			name := fmt.Sprintf("bench-g3-%d-w-%02d", target, iteration)
			if _, _, _, elapsed, err := runDemand(setupContext, apiClient, fixture.Demand(target, name), observed, mock); err != nil {
				t.Fatalf("target=%d warmup=%d: %v", target, iteration, err)
			} else if elapsed <= 0 {
				t.Fatalf("target=%d warmup=%d has invalid elapsed time", target, iteration)
			}
		}
		var evidenceDemand, evidenceGrant *unstructured.Unstructured
		var evidenceRequest, evidenceResponse map[string]any
		for iteration := 1; iteration <= samples; iteration++ {
			name := fmt.Sprintf("bench-g3-%d-s-%02d", target, iteration)
			demand, grant, exchange, elapsed, err := runDemand(setupContext, apiClient, fixture.Demand(target, name), observed, mock)
			if err != nil {
				t.Fatalf("target=%d sample=%d: %v", target, iteration, err)
			}
			if err := scale.ValidateGrant(grant, target); err != nil {
				t.Fatalf("target=%d sample=%d validation: %v", target, iteration, err)
			}
			byTarget[target] = append(byTarget[target], scale.Sample{Iteration: iteration, SelectedNodes: target, ElapsedMS: scale.DurationMS(elapsed), Success: true})
			evidenceDemand, evidenceGrant = demand, grant
			evidenceRequest, evidenceResponse = exchange.request, exchange.response
		}
		stats := scale.Summarize(groupName, boundary, target, byTarget[target])
		statistics = append(statistics, stats)
		t.Logf("target=%d mean=%.3fms p50=%.3fms p95=%.3fms", target, stats.MeanMS, stats.P50MS, stats.P95MS)
		evidence := filepath.Join(runDirectory, "evidence", fmt.Sprintf("select-%d", target))
		if err := base.WriteYAML(filepath.Join(evidence, "ngd.yaml"), evidenceDemand.Object); err != nil {
			t.Fatal(err)
		}
		if err := base.WriteJSON(filepath.Join(evidence, "prc-allocation-request.json"), evidenceRequest); err != nil {
			t.Fatal(err)
		}
		if err := base.WriteJSON(filepath.Join(evidence, "mock-algorithm-response.json"), evidenceResponse); err != nil {
			t.Fatal(err)
		}
		if err := base.WriteYAML(filepath.Join(evidence, "ngg.yaml"), evidenceGrant.Object); err != nil {
			t.Fatal(err)
		}
	}
	scale.WriteGroupResults(t, runDirectory, byTarget, statistics)
	t.Logf("results=%s", runDirectory)
}

type mockExchange struct{ request, response map[string]any }

func runDemand(ctx context.Context, apiClient client.Client, demand *unstructured.Unstructured, observed <-chan time.Time, mock *scaleMockAlgorithm) (*unstructured.Unstructured, *unstructured.Unstructured, mockExchange, time.Duration, error) {
	demand.SetUID("")
	demand.SetResourceVersion("")
	demand.SetCreationTimestamp(metav1.Time{})
	demand.SetGroupVersionKind(demandGVK)
	if err := apiClient.Create(ctx, demand); err != nil {
		return nil, nil, mockExchange{}, 0, err
	}
	var started time.Time
	select {
	case started = <-observed:
	case <-time.After(30 * time.Second):
		return nil, nil, mockExchange{}, 0, fmt.Errorf("timeout waiting for PRC observation")
	}
	grant := &unstructured.Unstructured{}
	grant.SetGroupVersionKind(grantGVK)
	waitContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := base.Eventually(waitContext, 5*time.Millisecond, func(callContext context.Context) (bool, error) {
		if err := apiClient.Get(callContext, client.ObjectKey{Name: "ngg-" + demand.GetName()}, grant); err != nil {
			return false, err
		}
		phase, _, _ := unstructured.NestedString(grant.Object, "status", "phase")
		return phase == "Active", nil
	}); err != nil {
		phase, _, _ := unstructured.NestedString(grant.Object, "status", "phase")
		nodes, _, _ := unstructured.NestedSlice(grant.Object, "spec", "nodes")
		failedDemand := &unstructured.Unstructured{}
		failedDemand.SetGroupVersionKind(demandGVK)
		_ = apiClient.Get(context.Background(), client.ObjectKey{Name: demand.GetName()}, failedDemand)
		exchange := mock.snapshot()
		return nil, nil, mockExchange{}, 0, fmt.Errorf("wait NGG Active: %w (phase=%q nodes=%d demandStatus=%v algorithmGroups=%d)", err, phase, len(nodes), failedDemand.Object["status"], candidateGroupCount(exchange.response))
	}
	elapsed := time.Since(started)
	finalDemand := &unstructured.Unstructured{}
	finalDemand.SetGroupVersionKind(demandGVK)
	if err := base.Eventually(waitContext, 5*time.Millisecond, func(callContext context.Context) (bool, error) {
		if err := apiClient.Get(callContext, client.ObjectKey{Name: demand.GetName()}, finalDemand); err != nil {
			return false, err
		}
		phase, _, _ := unstructured.NestedString(finalDemand.Object, "status", "phase")
		return phase == "Fulfilled", nil
	}); err != nil {
		return nil, nil, mockExchange{}, 0, err
	}
	exchange := mock.snapshot()
	demandCopy, grantCopy := finalDemand.DeepCopy(), grant.DeepCopy()
	_ = apiClient.Delete(ctx, grant)
	_ = apiClient.Delete(ctx, finalDemand)
	return demandCopy, grantCopy, exchange, elapsed, nil
}

func candidateGroupCount(response map[string]any) int {
	groups, _ := response["candidateNodeGroups"].([]any)
	return len(groups)
}

type scaleMockAlgorithm struct {
	server   *httptest.Server
	mu       sync.Mutex
	staticID string
	static   []any
	request  map[string]any
	response map[string]any
}

func newScaleMockAlgorithm() *scaleMockAlgorithm {
	mock := &scaleMockAlgorithm{}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.handle))
	return mock
}

func (m *scaleMockAlgorithm) handle(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(request.Body).Decode(&body)
	writer.Header().Set("Content-Type", "application/json")
	if request.Method == http.MethodGet && request.URL.Path == "/internal/v1/node-static-cache/status" {
		m.mu.Lock()
		id, count := m.staticID, len(m.static)
		m.mu.Unlock()
		_ = json.NewEncoder(writer).Encode(map[string]any{"algorithmBootId": "benchmark-mock-group3", "ready": id != "", "acceptedSnapshotId": id, "nodeCount": count})
		return
	}
	if request.Method == http.MethodPut && strings.HasPrefix(request.URL.Path, "/internal/v1/node-static-snapshots/") {
		id := strings.TrimPrefix(request.URL.Path, "/internal/v1/node-static-snapshots/")
		nodes, _ := body["nodes"].([]any)
		m.mu.Lock()
		m.staticID, m.static = id, nodes
		m.mu.Unlock()
		_ = json.NewEncoder(writer).Encode(map[string]any{"algorithmBootId": "benchmark-mock-group3", "acceptedSnapshotId": id, "nodeCount": len(nodes)})
		return
	}
	if request.Method != http.MethodPost || request.URL.Path != "/api/v1/allocate" {
		http.NotFound(writer, request)
		return
	}
	response, err := m.allocate(body)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	m.mu.Lock()
	m.request, m.response = body, response
	m.mu.Unlock()
	_ = json.NewEncoder(writer).Encode(response)
}

func (m *scaleMockAlgorithm) allocate(body map[string]any) (map[string]any, error) {
	ngd, _ := body["ngd"].(map[string]any)
	target := int(ngd["maxNodes"].(float64))
	m.mu.Lock()
	static := append([]any(nil), m.static...)
	m.mu.Unlock()
	nodes := make([]map[string]any, 0, len(static))
	for _, raw := range static {
		nodes = append(nodes, raw.(map[string]any))
	}
	if len(nodes) < target {
		return nil, fmt.Errorf("only %d Nodes available for target %d", len(nodes), target)
	}
	sort.Slice(nodes, func(i, j int) bool { return fmt.Sprint(nodes[i]["nodeName"]) < fmt.Sprint(nodes[j]["nodeName"]) })
	candidates := make([]any, 0, target)
	for _, node := range nodes[:target] {
		candidates = append(candidates, map[string]any{
			"nodeUID": node["nodeUID"], "nodeName": node["nodeName"], "score": int64(90),
			"resources": map[string]any{"cpuAvailable": "32", "memoryAvailable": "128Gi"},
			"topology":  map[string]any{"regionId": "CN-NORTH", "locationId": "HB-HL", "dataCenterId": "HB-HL-DC1", "roomId": "HB-HL-DC1-102", "borderDomainId": "HB-HL-DC1-102-BORDER-DOMAIN-01", "leafSwitchId": "leaf-001"},
		})
	}
	return map[string]any{
		"requestId": body["requestId"], "taskUID": body["taskUID"], "ngdUID": body["ngdUID"], "ngdGeneration": body["ngdGeneration"],
		"algorithmBootId": "benchmark-mock-group3", "nodeStaticSnapshotId": body["nodeStaticSnapshotId"], "schedulerStateSnapshotId": body["schedulerStateSnapshotId"],
		"metricSnapshotId": "benchmark-mock-metrics", "metricSnapshotCapturedAt": "2026-08-25T00:00:00Z", "topologySnapshotId": "mock-unicom-topology",
		"degraded": false, "warnings": []any{}, "status": "SUCCESS",
		"candidateNodeGroups": []any{map[string]any{"rank": int64(1), "groupId": "room:HB-HL-DC1-102", "topologyLevel": "room", "groupScore": 90.0, "nodes": candidates}},
	}, nil
}

func (m *scaleMockAlgorithm) snapshot() mockExchange {
	m.mu.Lock()
	defer m.mu.Unlock()
	return mockExchange{request: m.request, response: m.response}
}

func stopManager(t *testing.T, cancel context.CancelFunc, errors <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-errors:
		if err != nil {
			t.Errorf("manager stopped: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("manager did not stop")
	}
}

func benchmarkParameters(t *testing.T) ([]int, int, int) {
	t.Helper()
	targets, err := scale.Targets()
	if err != nil {
		t.Fatal(err)
	}
	samples, err := scale.Samples()
	if err != nil {
		t.Fatal(err)
	}
	warmups, err := scale.Warmups()
	if err != nil {
		t.Fatal(err)
	}
	return targets, samples, warmups
}

func currentDirectory(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve group directory")
	}
	return filepath.Dir(source)
}
