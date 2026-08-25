package group4_full_real_algorithm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	algorithm "demo.ngg/algorithm-server/algorithm"
	"demo.ngg/go-test-suites/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"scheduling.demo.ngg.io/prc/pkg/controller"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	demandGVK = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupDemand"}
	grantGVK  = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupGrant"}
)

func TestGroup4_PRCWatchesNGDCallsRealAlgorithmAndCreatesNGG(t *testing.T) {
	groupDirectory := currentGroupDirectory(t)
	fixture, err := common.GenerateFixture(1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := common.WriteFixtureInput(filepath.Join(groupDirectory, "testdata", "input"), fixture, true); err != nil {
		t.Fatal(err)
	}
	runDirectory := common.NewRunDirectory(t, groupDirectory)

	prometheus, err := common.NewMockPrometheus("go-test-prometheus-token", fixture.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	defer prometheus.Close()
	if err := prometheus.AssertAuthenticationBehavior(); err != nil {
		t.Fatalf("Mock Prometheus authentication: %v", err)
	}
	worker := common.PythonWorkerConfig(filepath.Join(runDirectory, "actual"))
	app, err := algorithm.NewApplication(algorithm.Config{
		BootID: "group4-algorithm", PrometheusURL: prometheus.URL(), PrometheusBearerToken: "go-test-prometheus-token",
		PrometheusClient: prometheus.Client(), DisableBackgroundMetrics: true,
		PythonExecutable: worker.Executable, PythonModule: worker.Module, PythonPath: worker.PythonPath, WorkerEvidenceDir: worker.EvidenceDir,
	})
	if err != nil {
		t.Fatalf("start real Algorithm: %v", err)
	}
	defer func() {
		if err := app.Close(); err != nil {
			t.Errorf("close Algorithm: %v", err)
		}
	}()
	if err := app.RefreshMetrics(context.Background()); err != nil {
		t.Fatalf("preload Prometheus: %v", err)
	}
	algorithmServer := httptest.NewServer(app.Handler())
	defer algorithmServer.Close()

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

	var exchangeMu sync.Mutex
	exchanges := []controller.AlgorithmExchange{}
	reconcileStarted := make(chan time.Time, 1)
	var reconcileStartOnce sync.Once
	manager, err := ctrl.NewManager(environment.Config, ctrl.Options{Scheme: environment.Scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", LeaderElection: false})
	if err != nil {
		t.Fatal(err)
	}
	reconciler := &controller.NodeGroupDemandReconciler{
		Client: manager.GetClient(), Scheme: manager.GetScheme(), AlgorithmURL: algorithmServer.URL,
		ClusterID: "mock-1000-node-cluster", HTTPClient: algorithmServer.Client(), DebugAlgorithmTrace: true,
		AlgorithmRecorder: func(exchange controller.AlgorithmExchange) {
			exchangeMu.Lock()
			exchanges = append(exchanges, exchange)
			exchangeMu.Unlock()
		},
		ReconcileObserver: func(_ string, _ int64, observedAt time.Time) {
			reconcileStartOnce.Do(func() { reconcileStarted <- observedAt })
		},
	}
	if err := reconciler.SetupWithManager(manager); err != nil {
		t.Fatalf("setup PRC controller: %v", err)
	}
	managerContext, managerCancel := context.WithCancel(context.Background())
	managerErrors := make(chan error, 1)
	go func() { managerErrors <- manager.Start(managerContext) }()
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
	if !manager.GetCache().WaitForCacheSync(setupContext) {
		t.Fatal("PRC cache did not sync")
	}

	demand := fixture.Demand.DeepCopy()
	demand.SetUID("")
	demand.SetResourceVersion("")
	demand.SetCreationTimestamp(metav1.Time{})
	demand.SetGroupVersionKind(demandGVK)
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
		grant.SetGroupVersionKind(grantGVK)
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
		finalDemand.SetGroupVersionKind(demandGVK)
		if err := apiClient.Get(ctx, client.ObjectKey{Name: "go-test-demand"}, finalDemand); err != nil {
			return false, err
		}
		demandPhase, _, _ := unstructured.NestedString(finalDemand.Object, "status", "phase")
		return demandPhase == "Fulfilled", nil
	})
	if err != nil {
		t.Fatalf("wait NGD Fulfilled: %v", err)
	}
	common.WriteTiming(t, runDirectory, "PRC observes NGD -> real Algorithm -> NGG Active", elapsed)

	exchangeMu.Lock()
	exchangeCopy := append([]controller.AlgorithmExchange(nil), exchanges...)
	exchangeMu.Unlock()
	algorithmRequest, algorithmResponse := findAllocateExchange(t, exchangeCopy)
	groups, ok := algorithmResponse["candidateNodeGroups"].([]any)
	if !ok || len(groups) == 0 || len(groups) > 3 {
		t.Fatalf("real Algorithm groups=%d, want 1..3", len(groups))
	}
	trace, ok := algorithmResponse["pipelineTrace"].([]any)
	if !ok || len(trace) != 3 {
		t.Fatalf("real Algorithm pipelineTrace=%d, want 3", len(trace))
	}
	for index, want := range []string{"requirement", "topology", "loadbalance"} {
		if got := fmt.Sprint(trace[index].(map[string]any)["algorithm"]); got != want {
			t.Fatalf("pipeline[%d]=%s, want %s", index, got, want)
		}
	}
	grantNodes, _, _ := unstructured.NestedSlice(grant.Object, "spec", "nodes")
	firstGroup := groups[0].(map[string]any)
	selectedNodes := firstGroup["nodes"].([]any)
	if len(grantNodes) != len(selectedNodes) {
		t.Fatalf("NGG nodes=%d, rank1 nodes=%d", len(grantNodes), len(selectedNodes))
	}
	for index := range grantNodes {
		grantName := fmt.Sprint(grantNodes[index].(map[string]any)["name"])
		algorithmName := fmt.Sprint(selectedNodes[index].(map[string]any)["nodeName"])
		if grantName != algorithmName {
			t.Fatalf("NGG node[%d]=%s, Algorithm=%s", index, grantName, algorithmName)
		}
	}
	if len(prometheus.Requests()) < 14 {
		t.Fatalf("Prometheus requests=%d, want at least 14", len(prometheus.Requests()))
	}

	actualDirectory := filepath.Join(runDirectory, "actual")
	_ = common.WriteYAML(filepath.Join(actualDirectory, "ngd-input.yaml"), demand.Object)
	_ = common.WriteJSON(filepath.Join(actualDirectory, "prometheus-requests.jsonl"), prometheus.Requests())
	_ = common.WriteJSON(filepath.Join(actualDirectory, "prc-allocation-request.json"), algorithmRequest)
	_ = common.WriteJSON(filepath.Join(actualDirectory, "prc-algorithm-http.json"), fullExchangeEvidence(exchangeCopy))
	_ = common.WriteJSON(filepath.Join(actualDirectory, "algorithm-response.json"), algorithmResponse)
	_ = common.WriteYAML(filepath.Join(actualDirectory, "ngg-raw.yaml"), grant.Object)
	_ = common.WriteYAML(filepath.Join(actualDirectory, "ngd-status.yaml"), map[string]any{"status": finalDemand.Object["status"]})
	comparison := map[string]any{"algorithmResponse": algorithmResponse, "ngg": grant.Object, "ngdStatus": finalDemand.Object["status"]}
	common.CompareGolden(t, filepath.Join(groupDirectory, "testdata", "expected", "full-result.json"), filepath.Join(actualDirectory, "full-result-normalized.json"), filepath.Join(runDirectory, "comparison", "diff.txt"), comparison)

}

func findAllocateExchange(t *testing.T, exchanges []controller.AlgorithmExchange) (map[string]any, map[string]any) {
	t.Helper()
	for index := len(exchanges) - 1; index >= 0; index-- {
		if exchanges[index].Path != "/api/v1/allocate" {
			continue
		}
		var request, response map[string]any
		if err := json.Unmarshal(exchanges[index].Request, &request); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(exchanges[index].Response, &response); err != nil {
			t.Fatal(err)
		}
		return request, response
	}
	t.Fatal("Allocate exchange not recorded")
	return nil, nil
}

func fullExchangeEvidence(exchanges []controller.AlgorithmExchange) []map[string]any {
	result := make([]map[string]any, 0, len(exchanges))
	for _, exchange := range exchanges {
		item := map[string]any{"method": exchange.Method, "path": exchange.Path, "statusCode": exchange.StatusCode}
		var request, response any
		_ = json.Unmarshal(exchange.Request, &request)
		_ = json.Unmarshal(exchange.Response, &response)
		item["request"] = request
		item["response"] = response
		result = append(result, item)
	}
	return result
}

func currentGroupDirectory(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve group directory")
	}
	return filepath.Dir(source)
}
