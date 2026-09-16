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

const groupName = "Group4-Full-RealAlgorithm"

var (
	demandGVK = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupDemand"}
	grantGVK  = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupGrant"}
)

func TestGroup4Scale3000(t *testing.T) {
	fixture, err := scale.GenerateFixture()
	if err != nil {
		t.Fatal(err)
	}
	if err := scale.WriteCanonicalInputs(scale.ScaleRoot(), fixture); err != nil {
		t.Fatal(err)
	}
	targets, samples, warmups := benchmarkParameters(t)
	runDirectory := scale.NewRunDirectory(t, currentDirectory(t))

	prometheus, err := base.NewMockPrometheus("benchmark-prometheus-token", fixture.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	defer prometheus.Close()
	worker := base.PythonWorkerConfig("")
	app, err := algorithm.NewApplication(algorithm.Config{
		BootID: "benchmark-group4", PrometheusURL: prometheus.URL(), PrometheusBearerToken: "benchmark-prometheus-token",
		PrometheusClient: prometheus.Client(), DisableBackgroundMetrics: true, TopologyConfigData: fixture.TopologyConfig,
		PythonExecutable: worker.Executable, PythonModule: worker.Module, PythonPath: worker.PythonPath,
	})
	if err != nil {
		t.Fatal(err)
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
	if err := base.CreateKubernetesInputs(setupContext, apiClient, &base.Fixture{Nodes: fixture.Nodes}); err != nil {
		t.Fatal(err)
	}

	observed := make(chan time.Time, 1)
	var exchangeMu sync.Mutex
	var lastAllocate controller.AlgorithmExchange
	manager, err := ctrl.NewManager(environment.Config, ctrl.Options{Scheme: environment.Scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", LeaderElection: false})
	if err != nil {
		t.Fatal(err)
	}
	staticSnapshots := controller.NewStaticSnapshotState()
	staticReconciler := &controller.NodeStaticSnapshotReconciler{
		Client: manager.GetClient(), AlgorithmURL: algorithmServer.URL, ClusterID: scale.ClusterID,
		HTTPClient: algorithmServer.Client(), State: staticSnapshots,
	}
	if err := staticReconciler.SetupWithManager(manager); err != nil {
		t.Fatal(err)
	}
	processor := &controller.DemandProcessor{
		Client: manager.GetClient(), AlgorithmURL: algorithmServer.URL,
		HTTPClient: algorithmServer.Client(), StaticSnapshots: staticSnapshots, DebugAlgorithmTrace: false,
		ReconcileObserver: func(_ string, _ int64, at time.Time) {
			select {
			case observed <- at:
			default:
			}
		},
		AlgorithmRecorder: func(exchange controller.AlgorithmExchange) {
			if exchange.Path != "/api/v1/allocate" {
				return
			}
			exchangeMu.Lock()
			lastAllocate = exchange
			exchangeMu.Unlock()
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

	boundary := "PRC observes NGD with static/metrics pre-synced -> dynamic state/real Algorithm/Python -> NGG Active"
	byTarget := map[int][]scale.Sample{}
	statistics := []scale.Statistics{}
	for _, target := range targets {
		for iteration := 1; iteration <= warmups; iteration++ {
			name := fmt.Sprintf("bench-g4-%d-w-%02d", target, iteration)
			if _, _, _, elapsed, err := runDemand(setupContext, apiClient, fixture.Demand(target, name), observed, &exchangeMu, &lastAllocate); err != nil {
				t.Fatalf("target=%d warmup=%d: %v", target, iteration, err)
			} else if elapsed <= 0 {
				t.Fatalf("target=%d warmup=%d has invalid elapsed time", target, iteration)
			}
		}
		var evidenceDemand, evidenceGrant *unstructured.Unstructured
		var evidenceExchange controller.AlgorithmExchange
		for iteration := 1; iteration <= samples; iteration++ {
			name := fmt.Sprintf("bench-g4-%d-s-%02d", target, iteration)
			demand, grant, exchange, elapsed, err := runDemand(setupContext, apiClient, fixture.Demand(target, name), observed, &exchangeMu, &lastAllocate)
			if err != nil {
				t.Fatalf("target=%d sample=%d: %v", target, iteration, err)
			}
			if err := scale.ValidateGrant(grant, target); err != nil {
				t.Fatalf("target=%d sample=%d validation: %v", target, iteration, err)
			}
			var response controller.AlgorithmResponse
			if err := json.Unmarshal(exchange.Response, &response); err != nil {
				t.Fatalf("decode Algorithm response: %v", err)
			}
			if err := scale.ValidateResponse(response, target); err != nil {
				t.Fatalf("target=%d sample=%d Algorithm validation: %v", target, iteration, err)
			}
			byTarget[target] = append(byTarget[target], scale.Sample{Iteration: iteration, SelectedNodes: target, ElapsedMS: scale.DurationMS(elapsed), Success: true})
			evidenceDemand, evidenceGrant, evidenceExchange = demand, grant, exchange
		}
		stats := scale.Summarize(groupName, boundary, target, byTarget[target])
		statistics = append(statistics, stats)
		t.Logf("target=%d mean=%.3fms p50=%.3fms p95=%.3fms", target, stats.MeanMS, stats.P50MS, stats.P95MS)
		evidence := filepath.Join(runDirectory, "evidence", fmt.Sprintf("select-%d", target))
		if err := base.WriteYAML(filepath.Join(evidence, "ngd.yaml"), evidenceDemand.Object); err != nil {
			t.Fatal(err)
		}
		var requestBody, responseBody any
		_ = json.Unmarshal(evidenceExchange.Request, &requestBody)
		_ = json.Unmarshal(evidenceExchange.Response, &responseBody)
		if err := base.WriteJSON(filepath.Join(evidence, "prc-allocation-request.json"), requestBody); err != nil {
			t.Fatal(err)
		}
		if err := base.WriteJSON(filepath.Join(evidence, "algorithm-response.json"), responseBody); err != nil {
			t.Fatal(err)
		}
		if err := base.WriteYAML(filepath.Join(evidence, "ngg.yaml"), evidenceGrant.Object); err != nil {
			t.Fatal(err)
		}
	}
	scale.WriteGroupResults(t, runDirectory, byTarget, statistics)
	t.Logf("results=%s", runDirectory)
}

func runDemand(ctx context.Context, apiClient client.Client, demand *unstructured.Unstructured, observed <-chan time.Time, exchangeMu *sync.Mutex, lastAllocate *controller.AlgorithmExchange) (*unstructured.Unstructured, *unstructured.Unstructured, controller.AlgorithmExchange, time.Duration, error) {
	demand.SetUID("")
	demand.SetResourceVersion("")
	demand.SetCreationTimestamp(metav1.Time{})
	demand.SetGroupVersionKind(demandGVK)
	if err := apiClient.Create(ctx, demand); err != nil {
		return nil, nil, controller.AlgorithmExchange{}, 0, err
	}
	var started time.Time
	select {
	case started = <-observed:
	case <-time.After(30 * time.Second):
		return nil, nil, controller.AlgorithmExchange{}, 0, fmt.Errorf("timeout waiting for PRC observation")
	}
	grant := &unstructured.Unstructured{}
	grant.SetGroupVersionKind(grantGVK)
	waitContext, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := base.Eventually(waitContext, 5*time.Millisecond, func(callContext context.Context) (bool, error) {
		if err := apiClient.Get(callContext, client.ObjectKey{Name: "ngg-" + demand.GetName()}, grant); err != nil {
			return false, err
		}
		phase, _, _ := unstructured.NestedString(grant.Object, "status", "phase")
		return phase == "Active", nil
	}); err != nil {
		return nil, nil, controller.AlgorithmExchange{}, 0, err
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
		return nil, nil, controller.AlgorithmExchange{}, 0, err
	}
	exchangeMu.Lock()
	exchange := *lastAllocate
	exchangeMu.Unlock()
	if len(exchange.Response) == 0 {
		return nil, nil, controller.AlgorithmExchange{}, 0, fmt.Errorf("Algorithm Allocate exchange was not recorded")
	}
	demandCopy, grantCopy := finalDemand.DeepCopy(), grant.DeepCopy()
	_ = apiClient.Delete(ctx, grant)
	_ = apiClient.Delete(ctx, finalDemand)
	return demandCopy, grantCopy, exchange, elapsed, nil
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
