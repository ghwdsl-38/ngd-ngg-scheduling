package group4_full_real_algorithm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	algorithm "demo.ngg/algorithm-server/algorithm"
	"demo.ngg/go-test-suites/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	prcapp "scheduling.demo.ngg.io/prc/pkg/application"
	"scheduling.demo.ngg.io/prc/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	demandGVK = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupDemand"}
	grantGVK  = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupGrant"}
)

func TestGroup4_PRCWatchesNGDCallsRealAlgorithmAndCreatesNGG(t *testing.T) {
	debugMode := os.Getenv("NGG_TEST_DEBUG") == "true"
	setupTimeout := 90 * time.Second
	waitTimeout := 30 * time.Second
	managerShutdownTimeout := 5 * time.Second
	metricsStaleAfter := 120 * time.Second
	algorithmTimeoutSeconds := int64(5)
	if debugMode {
		debugTimeout := 10 * time.Minute
		if configured := os.Getenv("NGG_TEST_DEBUG_TIMEOUT"); configured != "" {
			parsed, err := time.ParseDuration(configured)
			if err != nil || parsed < time.Second {
				t.Fatalf("invalid NGG_TEST_DEBUG_TIMEOUT %q: must be a duration of at least 1s", configured)
			}
			debugTimeout = parsed
		}
		setupTimeout = debugTimeout
		waitTimeout = debugTimeout
		managerShutdownTimeout = debugTimeout
		// Delve会暂停包括15秒指标刷新协程在内的整个Go进程。Debug模式下
		// 将快照有效期覆盖完整测试窗口，避免断点停留被误判成生产指标过期。
		metricsStaleAfter = 3 * debugTimeout
		algorithmTimeoutSeconds = int64(debugTimeout / time.Second)
		t.Logf("Group4 debug mode enabled: internal timeouts=%s; elapsed time is not a performance result", debugTimeout)
	}

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
	algorithmServer, err := algorithm.NewServer(algorithm.Config{
		BootID: "group4-algorithm", PrometheusURL: prometheus.URL(), PrometheusBearerToken: "go-test-prometheus-token",
		PrometheusClient: prometheus.Client(), MetricsStaleAfter: metricsStaleAfter, TopologyConfigData: fixture.TopologyConfig,
		PythonExecutable: worker.Executable, PythonModule: worker.Module, PythonPath: worker.PythonPath, WorkerEvidenceDir: worker.EvidenceDir,
	}, algorithm.ServerOptions{ListenAddress: "127.0.0.1:0", ShutdownTimeout: managerShutdownTimeout})
	if err != nil {
		t.Fatalf("create real Algorithm Server: %v", err)
	}
	algorithmContext, algorithmCancel := context.WithCancel(context.Background())
	if err := algorithmServer.Start(algorithmContext); err != nil {
		t.Fatalf("start real Algorithm Server: %v", err)
	}
	defer func() {
		algorithmCancel()
		shutdown, cancel := context.WithTimeout(context.Background(), managerShutdownTimeout)
		defer cancel()
		if err := algorithmServer.Close(shutdown); err != nil {
			t.Errorf("close Algorithm Server: %v", err)
		}
		if err := algorithmServer.Wait(); err != nil {
			t.Errorf("wait Algorithm Server: %v", err)
		}
	}()
	algorithmReadyContext, algorithmReadyCancel := context.WithTimeout(context.Background(), setupTimeout)
	defer algorithmReadyCancel()
	if err := algorithmServer.WaitForReady(algorithmReadyContext); err != nil {
		t.Fatalf("wait Algorithm Server ready after Prometheus preload: %v", err)
	}

	environment := common.StartEnvTest(t)
	defer environment.Stop(t)
	apiClient, err := client.New(environment.Config, client.Options{Scheme: environment.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	setupContext, setupCancel := context.WithTimeout(context.Background(), setupTimeout)
	defer setupCancel()
	if err := common.CreateKubernetesInputs(setupContext, apiClient, fixture); err != nil {
		t.Fatal(err)
	}

	var exchangeMu sync.Mutex
	exchanges := []controller.AlgorithmExchange{}
	reconcileStarted := make(chan time.Time, 1)
	var reconcileStartOnce sync.Once
	algorithmRecorder := func(exchange controller.AlgorithmExchange) {
		exchangeMu.Lock()
		exchanges = append(exchanges, exchange)
		exchangeMu.Unlock()
	}
	prcApplication, err := prcapp.New(prcapp.Config{
		KubernetesConfig: environment.Config, Scheme: environment.Scheme,
		AlgorithmURL: algorithmServer.URL(), ClusterID: "mock-1000-node-cluster", HTTPClient: algorithmServer.HTTPClient(),
		MetricsBindAddress: "0", HealthProbeBindAddress: "0", LeaderElection: false,
		DebugAlgorithmTrace: true, AlgorithmRecorder: algorithmRecorder,
		ReconcileObserver: func(_ string, _ int64, observedAt time.Time) {
			reconcileStartOnce.Do(func() { reconcileStarted <- observedAt })
		},
	})
	if err != nil {
		t.Fatalf("create PRC Application: %v", err)
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
		case <-time.After(managerShutdownTimeout):
			t.Error("manager did not stop")
		}
	}()
	if _, err := prcApplication.WaitForReady(setupContext); err != nil {
		t.Fatalf("PRC Application did not become ready: %v", err)
	}
	exchangeMu.Lock()
	putCountBeforeDemand := countMethodPath(exchanges, "PUT", "/internal/v1/node-static-snapshots/")
	exchangeMu.Unlock()

	demand := fixture.Demand.DeepCopy()
	demand.SetUID("")
	demand.SetResourceVersion("")
	demand.SetCreationTimestamp(metav1.Time{})
	demand.SetGroupVersionKind(demandGVK)
	if debugMode {
		// Delve暂停Go进程时墙上时钟仍继续。仅在Debug测试输入中放宽PRC调用
		// Algorithm的请求超时，避免停在HTTP或Go/Python边界时请求先被取消。
		spec, _, _ := unstructured.NestedMap(demand.Object, "spec")
		grantPolicy, _, _ := unstructured.NestedMap(spec, "grantPolicy")
		if grantPolicy == nil {
			grantPolicy = map[string]any{}
		}
		grantPolicy["algorithmTimeoutSeconds"] = algorithmTimeoutSeconds
		spec["grantPolicy"] = grantPolicy
		if err := unstructured.SetNestedMap(demand.Object, spec, "spec"); err != nil {
			t.Fatalf("set debug Algorithm timeout: %v", err)
		}
	}
	if err := apiClient.Create(setupContext, demand); err != nil {
		t.Fatalf("create formal NGD: %v", err)
	}
	waitContext, waitCancel := context.WithTimeout(context.Background(), waitTimeout)
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
	// Debug边界：NGG已经由PRC创建并更新为Active后，再从Kubernetes API
	// Server明确读取一次持久化对象。该GET只执行一次，适合作为全链路最后一个断点。
	persistedGrant := &unstructured.Unstructured{}
	persistedGrant.SetGroupVersionKind(grantGVK)
	if err := apiClient.Get(waitContext, client.ObjectKey{Name: "ngg-go-test-demand"}, persistedGrant); err != nil {
		t.Fatalf("get persisted NGG from Kubernetes: %v", err)
	}
	grant = persistedGrant
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
	if debugMode {
		if err := common.WriteJSON(filepath.Join(runDirectory, "debug-timing.json"), map[string]any{
			"debug": true, "performanceValid": false, "unit": "ms",
			"boundary":  "PRC observes NGD -> real Algorithm -> NGG Active",
			"elapsedMs": float64(elapsed.Microseconds()) / 1000,
			"note":      "includes time paused at Delve breakpoints",
		}); err != nil {
			t.Fatalf("write debug timing: %v", err)
		}
		t.Logf("debugTiming performanceValid=false elapsedMs=%.3f", float64(elapsed.Microseconds())/1000)
	} else {
		common.WriteTiming(t, runDirectory, "PRC observes NGD -> real Algorithm -> NGG Active", elapsed)
	}

	exchangeMu.Lock()
	exchangeCopy := append([]controller.AlgorithmExchange(nil), exchanges...)
	exchangeMu.Unlock()
	if got := countMethodPath(exchangeCopy, "PUT", "/internal/v1/node-static-snapshots/"); got != putCountBeforeDemand {
		t.Fatalf("NGD Reconcile issued static PUTs: before=%d after=%d", putCountBeforeDemand, got)
	}
	algorithmRequest, algorithmResponse := findAllocateExchange(t, exchangeCopy)
	requestNGD, ok := algorithmRequest["ngd"].(map[string]any)
	if !ok {
		t.Fatal("PRC Allocate request does not contain the original NGD spec")
	}
	topologyLabels, ok := requestNGD["topologyLabels"].(map[string]any)
	if !ok || len(topologyLabels) != 5 {
		t.Fatalf("PRC topologyLabels=%v, want all five China Unicom levels", requestNGD["topologyLabels"])
	}
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
	if got := fmt.Sprint(firstGroup["topologyLevel"]); got != "leafDomain" {
		t.Fatalf("topologyLevel=%s, want leafDomain because leaf-switch=requiredSame", got)
	}
	warnings, _ := algorithmResponse["warnings"].([]any)
	if len(warnings) != 1 || !strings.Contains(fmt.Sprint(warnings[0]), "SPINE_NOT_FOUND_FALLBACK") {
		t.Fatalf("warnings=%v, want missing-Spine to Border-Same fallback", warnings)
	}
	selectedNodes := firstGroup["nodes"].([]any)
	if len(grantNodes) != len(selectedNodes) {
		t.Fatalf("NGG nodes=%d, rank1 nodes=%d", len(grantNodes), len(selectedNodes))
	}
	for index := range grantNodes {
		grantNode := grantNodes[index].(map[string]any)
		grantName := fmt.Sprint(grantNode["name"])
		algorithmName := fmt.Sprint(selectedNodes[index].(map[string]any)["nodeName"])
		if grantName != algorithmName {
			t.Fatalf("NGG node[%d]=%s, Algorithm=%s", index, grantName, algorithmName)
		}
		topology, _ := grantNode["topology"].(map[string]any)
		for _, field := range []string{"dataCenter", "room", "borderSwitch", "leafSwitch"} {
			if fmt.Sprint(topology[field]) == "" {
				t.Fatalf("NGG node[%d] topology.%s is empty: %v", index, field, topology)
			}
		}
		if _, found := topology["spineSwitch"]; found {
			t.Fatalf("NGG must omit spineSwitch when configured SPINE is empty: %v", topology)
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

func countMethodPath(exchanges []controller.AlgorithmExchange, method, pathPrefix string) int {
	count := 0
	for _, exchange := range exchanges {
		if exchange.Method == method && strings.HasPrefix(exchange.Path, pathPrefix) {
			count++
		}
	}
	return count
}

func currentGroupDirectory(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve group directory")
	}
	return filepath.Dir(source)
}
