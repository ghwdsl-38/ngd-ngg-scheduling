package group5_prc_refresh_lifecycle_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	algorithm "demo.ngg/algorithm-server/algorithm"
	"demo.ngg/go-test-suites/common"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	prcapp "scheduling.demo.ngg.io/prc/pkg/application"
	"scheduling.demo.ngg.io/prc/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	demandGVK = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupDemand"}
	grantGVK  = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupGrant"}
)

const (
	demandName      = "go-test-demand"
	grantName       = "ngg-go-test-demand"
	refreshInterval = 250 * time.Millisecond
)

// TestGroup5 verifies the repeated PRC lifecycle against real envtest,
// Algorithm Go server, Python worker and authenticated Mock Prometheus:
// initial calculation -> unchanged periodic refresh -> NGD spec update ->
// NGG update -> NGD deletion -> timer cancellation and NGG deletion.
func TestGroup5_PRCPeriodicRefreshUpdateAndDelete(t *testing.T) {
	groupDirectory := currentGroupDirectory(t)
	runDirectory := common.NewRunDirectory(t, groupDirectory)
	fixture, err := common.GenerateFixture(1000)
	if err != nil {
		t.Fatal(err)
	}

	prometheus, err := common.NewMockPrometheus("go-test-prometheus-token", fixture.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	defer prometheus.Close()
	worker := common.PythonWorkerConfig(filepath.Join(runDirectory, "actual", "python-worker"))
	algorithmServer, err := algorithm.NewServer(algorithm.Config{
		BootID: "group5-algorithm", PrometheusURL: prometheus.URL(), PrometheusBearerToken: "go-test-prometheus-token",
		PrometheusClient: prometheus.Client(), MetricsStaleAfter: 2 * time.Minute, TopologyConfigData: fixture.TopologyConfig,
		PythonExecutable: worker.Executable, PythonModule: worker.Module, PythonPath: worker.PythonPath, WorkerEvidenceDir: worker.EvidenceDir,
	}, algorithm.ServerOptions{ListenAddress: "127.0.0.1:0", ShutdownTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("create Algorithm Server: %v", err)
	}
	algorithmContext, algorithmCancel := context.WithCancel(context.Background())
	if err := algorithmServer.Start(algorithmContext); err != nil {
		t.Fatalf("start Algorithm Server: %v", err)
	}
	defer func() {
		algorithmCancel()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := algorithmServer.Close(shutdown); err != nil {
			t.Errorf("close Algorithm Server: %v", err)
		}
		if err := algorithmServer.Wait(); err != nil {
			t.Errorf("wait Algorithm Server: %v", err)
		}
	}()

	setupContext, setupCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer setupCancel()
	if err := algorithmServer.WaitForReady(setupContext); err != nil {
		t.Fatalf("wait Algorithm Server ready: %v", err)
	}
	environment := common.StartEnvTest(t)
	defer environment.Stop(t)
	apiClient, err := client.New(environment.Config, client.Options{Scheme: environment.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	if err := common.CreateKubernetesInputs(setupContext, apiClient, fixture); err != nil {
		t.Fatal(err)
	}

	var exchangeMu sync.Mutex
	exchanges := []controller.AlgorithmExchange{}
	processorStarts := make(chan time.Time, 16)
	prcApplication, err := prcapp.New(prcapp.Config{
		KubernetesConfig: environment.Config, Scheme: environment.Scheme,
		AlgorithmURL: algorithmServer.URL(), ClusterID: "mock-1000-node-cluster", HTTPClient: algorithmServer.HTTPClient(),
		MetricsBindAddress: "0", HealthProbeBindAddress: "0", LeaderElection: false,
		DemandRefreshInterval: refreshInterval, MaxConcurrentRefreshes: 3,
		DebugAlgorithmTrace: true,
		AlgorithmRecorder: func(exchange controller.AlgorithmExchange) {
			exchangeMu.Lock()
			exchanges = append(exchanges, exchange)
			exchangeMu.Unlock()
		},
		ReconcileObserver: func(_ string, _ int64, observedAt time.Time) {
			select {
			case processorStarts <- observedAt:
			default:
			}
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
		case <-time.After(5 * time.Second):
			t.Error("manager did not stop")
		}
	}()
	if _, err := prcApplication.WaitForReady(setupContext); err != nil {
		t.Fatalf("PRC Application did not become ready: %v", err)
	}

	demand := fixture.Demand.DeepCopy()
	demand.SetUID("")
	demand.SetResourceVersion("")
	demand.SetCreationTimestamp(metav1.Time{})
	demand.SetGroupVersionKind(demandGVK)
	createdAt := time.Now()
	if err := apiClient.Create(setupContext, demand); err != nil {
		t.Fatalf("create NGD: %v", err)
	}
	waitContext, waitCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer waitCancel()
	select {
	case started := <-processorStarts:
		t.Logf("initial Demand Processor observed NGD after %.3fms", elapsedMillis(createdAt, started))
	case <-time.After(10 * time.Second):
		t.Fatal("Refresh Controller did not invoke Demand Processor within 10s")
	}

	initialGrant := waitForGrant(t, waitContext, apiClient, 10)
	initialDemand := waitForDemandPhase(t, waitContext, apiClient, "Fulfilled")
	initialCompletedAt := time.Now()
	if initialDemand.GetGeneration() != 1 {
		t.Fatalf("initial NGD generation=%d, want 1", initialDemand.GetGeneration())
	}
	assertGrantOwnership(t, initialGrant, initialDemand.GetUID())

	// Simulate the consumer owning status.consumer. Subsequent PRC SSA refreshes
	// must leave this subtree untouched.
	consumerStatus := map[string]any{
		"consumer": map[string]any{
			"acceptedGeneration": initialGrant.GetGeneration(),
			"used":               map[string]any{"cpu": "32", "memory": "128Gi"},
		},
	}
	if err := applyConsumerStatus(waitContext, apiClient, consumerStatus); err != nil {
		t.Fatalf("apply consumer status: %v", err)
	}

	initialCalls := allocateCount(snapshotExchanges(&exchangeMu, &exchanges))
	if initialCalls < 1 {
		t.Fatal("initial calculation did not call Algorithm allocate")
	}
	periodicStartedAt := time.Now()
	waitForAllocateCount(t, waitContext, &exchangeMu, &exchanges, initialCalls+1)
	initialLastUpdated, _, _ := unstructured.NestedString(initialDemand.Object, "status", "lastUpdated")
	periodicDemand := waitForDemandRefresh(t, waitContext, apiClient, initialLastUpdated)
	periodicGrant := waitForGrant(t, waitContext, apiClient, 10)
	periodicCompletedAt := time.Now()
	if periodicDemand.GetGeneration() != initialDemand.GetGeneration() {
		t.Fatalf("periodic refresh changed NGD generation: before=%d after=%d", initialDemand.GetGeneration(), periodicDemand.GetGeneration())
	}
	actualConsumer, found, _ := unstructured.NestedMap(periodicGrant.Object, "status", "consumer")
	if !found || fmt.Sprint(actualConsumer["acceptedGeneration"]) != fmt.Sprint(initialGrant.GetGeneration()) {
		t.Fatalf("PRC refresh overwrote consumer status: %#v", actualConsumer)
	}

	// Reduce the resource demand from 10 to 6 Nodes while the old NGG remains
	// available. The new generation must update the same NGG object.
	latestDemand := &unstructured.Unstructured{}
	latestDemand.SetGroupVersionKind(demandGVK)
	if err := apiClient.Get(waitContext, client.ObjectKey{Name: demandName}, latestDemand); err != nil {
		t.Fatalf("get NGD for update: %v", err)
	}
	spec, _, _ := unstructured.NestedMap(latestDemand.Object, "spec")
	spec["maxNodes"] = int64(6)
	spec["quota"] = map[string]any{"cpu": "192", "memory": "768Gi"}
	spec["minResources"] = map[string]any{"cpu": "192", "memory": "768Gi"}
	if err := unstructured.SetNestedMap(latestDemand.Object, spec, "spec"); err != nil {
		t.Fatal(err)
	}
	updateStartedAt := time.Now()
	if err := apiClient.Update(waitContext, latestDemand); err != nil {
		t.Fatalf("update NGD spec: %v", err)
	}
	updatedGrant := waitForGrant(t, waitContext, apiClient, 6)
	updatedDemand := waitForDemandPhase(t, waitContext, apiClient, "Fulfilled")
	updateCompletedAt := time.Now()
	if updatedDemand.GetGeneration() != 2 {
		t.Fatalf("updated NGD generation=%d, want 2", updatedDemand.GetGeneration())
	}
	if updatedGrant.GetUID() != initialGrant.GetUID() {
		t.Fatalf("NGD update recreated NGG: initialUID=%s updatedUID=%s", initialGrant.GetUID(), updatedGrant.GetUID())
	}
	actualConsumer, found, _ = unstructured.NestedMap(updatedGrant.Object, "status", "consumer")
	if !found || fmt.Sprint(actualConsumer["acceptedGeneration"]) != fmt.Sprint(initialGrant.GetGeneration()) {
		t.Fatalf("NGD update overwrote consumer status: %#v", actualConsumer)
	}

	deleteStartedAt := time.Now()
	if err := apiClient.Delete(waitContext, updatedDemand); err != nil {
		t.Fatalf("delete NGD: %v", err)
	}
	waitForGrantDeleted(t, waitContext, apiClient)
	deleteCompletedAt := time.Now()
	// Let any event that was already queued drain, then prove the scheduler no
	// longer produces periodic calls for the deleted NGD.
	time.Sleep(2 * refreshInterval)
	callsAfterDeleteSettled := allocateCount(snapshotExchanges(&exchangeMu, &exchanges))
	time.Sleep(2 * refreshInterval)
	if got := allocateCount(snapshotExchanges(&exchangeMu, &exchanges)); got != callsAfterDeleteSettled {
		t.Fatalf("Algorithm calls continued after NGD deletion: settled=%d later=%d", callsAfterDeleteSettled, got)
	}

	actualDirectory := filepath.Join(runDirectory, "actual")
	_ = common.WriteYAML(filepath.Join(actualDirectory, "01-created-ngd.yaml"), demand.Object)
	_ = common.WriteYAML(filepath.Join(actualDirectory, "02-initial-ngg.yaml"), initialGrant.Object)
	_ = common.WriteYAML(filepath.Join(actualDirectory, "03-periodic-ngg.yaml"), periodicGrant.Object)
	_ = common.WriteYAML(filepath.Join(actualDirectory, "04-updated-ngd.yaml"), updatedDemand.Object)
	_ = common.WriteYAML(filepath.Join(actualDirectory, "05-updated-ngg.yaml"), updatedGrant.Object)
	_ = common.WriteYAML(filepath.Join(actualDirectory, "06-consumer-status.yaml"), map[string]any{"status": actualConsumer})
	_ = common.WriteJSON(filepath.Join(actualDirectory, "07-algorithm-exchanges.json"), exchangeEvidence(snapshotExchanges(&exchangeMu, &exchanges)))
	_ = common.WriteJSON(filepath.Join(actualDirectory, "08-lifecycle-summary.json"), map[string]any{
		"refreshInterval": refreshInterval.String(), "algorithmAllocateCalls": callsAfterDeleteSettled,
		"initial":  map[string]any{"ngdGeneration": initialDemand.GetGeneration(), "nggGeneration": initialGrant.GetGeneration(), "nodeCount": 10, "elapsedMs": elapsedMillis(createdAt, initialCompletedAt)},
		"periodic": map[string]any{"ngdGeneration": periodicDemand.GetGeneration(), "nggGeneration": periodicGrant.GetGeneration(), "nodeCount": 10, "elapsedMs": elapsedMillis(periodicStartedAt, periodicCompletedAt)},
		"update":   map[string]any{"ngdGeneration": updatedDemand.GetGeneration(), "nggGeneration": updatedGrant.GetGeneration(), "nodeCount": 6, "elapsedMs": elapsedMillis(updateStartedAt, updateCompletedAt)},
		"delete":   map[string]any{"nggDeleted": true, "elapsedMs": elapsedMillis(deleteStartedAt, deleteCompletedAt)},
	})
	t.Logf("initial=%.3fms periodic=%.3fms update=%.3fms delete=%.3fms calls=%d", elapsedMillis(createdAt, initialCompletedAt), elapsedMillis(periodicStartedAt, periodicCompletedAt), elapsedMillis(updateStartedAt, updateCompletedAt), elapsedMillis(deleteStartedAt, deleteCompletedAt), callsAfterDeleteSettled)
}

func waitForGrant(t *testing.T, ctx context.Context, apiClient client.Client, nodeCount int) *unstructured.Unstructured {
	t.Helper()
	var result *unstructured.Unstructured
	err := common.Eventually(ctx, 10*time.Millisecond, func(ctx context.Context) (bool, error) {
		grant := &unstructured.Unstructured{}
		grant.SetGroupVersionKind(grantGVK)
		if err := apiClient.Get(ctx, client.ObjectKey{Name: grantName}, grant); err != nil {
			return false, err
		}
		phase, _, _ := unstructured.NestedString(grant.Object, "status", "phase")
		nodes, _, _ := unstructured.NestedSlice(grant.Object, "spec", "nodes")
		if phase == "Active" && len(nodes) == nodeCount {
			result = grant.DeepCopy()
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		demand := &unstructured.Unstructured{}
		demand.SetGroupVersionKind(demandGVK)
		if getErr := apiClient.Get(context.Background(), client.ObjectKey{Name: demandName}, demand); getErr == nil {
			t.Logf("NGD at timeout: generation=%d status=%#v", demand.GetGeneration(), demand.Object["status"])
		}
		t.Fatalf("wait NGG Active with %d nodes: %v", nodeCount, err)
	}
	return result
}

func waitForDemandPhase(t *testing.T, ctx context.Context, apiClient client.Client, phase string) *unstructured.Unstructured {
	t.Helper()
	var result *unstructured.Unstructured
	err := common.Eventually(ctx, 10*time.Millisecond, func(ctx context.Context) (bool, error) {
		demand := &unstructured.Unstructured{}
		demand.SetGroupVersionKind(demandGVK)
		if err := apiClient.Get(ctx, client.ObjectKey{Name: demandName}, demand); err != nil {
			return false, err
		}
		actual, _, _ := unstructured.NestedString(demand.Object, "status", "phase")
		if actual == phase {
			result = demand.DeepCopy()
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("wait NGD phase %s: %v", phase, err)
	}
	return result
}

func waitForDemandRefresh(t *testing.T, ctx context.Context, apiClient client.Client, previousLastUpdated string) *unstructured.Unstructured {
	t.Helper()
	var result *unstructured.Unstructured
	err := common.Eventually(ctx, 10*time.Millisecond, func(ctx context.Context) (bool, error) {
		demand := &unstructured.Unstructured{}
		demand.SetGroupVersionKind(demandGVK)
		if err := apiClient.Get(ctx, client.ObjectKey{Name: demandName}, demand); err != nil {
			return false, err
		}
		phase, _, _ := unstructured.NestedString(demand.Object, "status", "phase")
		lastUpdated, _, _ := unstructured.NestedString(demand.Object, "status", "lastUpdated")
		if phase == "Fulfilled" && lastUpdated != "" && lastUpdated != previousLastUpdated {
			result = demand.DeepCopy()
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("wait periodic NGD status refresh after %s: %v", previousLastUpdated, err)
	}
	return result
}

func waitForGrantDeleted(t *testing.T, ctx context.Context, apiClient client.Client) {
	t.Helper()
	err := common.Eventually(ctx, 10*time.Millisecond, func(ctx context.Context) (bool, error) {
		grant := &unstructured.Unstructured{}
		grant.SetGroupVersionKind(grantGVK)
		err := apiClient.Get(ctx, client.ObjectKey{Name: grantName}, grant)
		return apierrors.IsNotFound(err), nil
	})
	if err != nil {
		t.Fatalf("wait NGG deletion: %v", err)
	}
}

func applyConsumerStatus(ctx context.Context, apiClient client.Client, status map[string]any) error {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(grantGVK)
	object.SetName(grantName)
	if err := unstructured.SetNestedMap(object.Object, status, "status"); err != nil {
		return err
	}
	return apiClient.Status().Apply(ctx, client.ApplyConfigurationFromUnstructured(object), client.FieldOwner("group5-consumer-status"))
}

func assertGrantOwnership(t *testing.T, grant *unstructured.Unstructured, demandUID types.UID) {
	t.Helper()
	owners := grant.GetOwnerReferences()
	if len(owners) != 1 || owners[0].UID != demandUID || owners[0].Kind != "NodeGroupDemand" {
		t.Fatalf("NGG ownerReference=%#v, want formal NGD UID %s", owners, demandUID)
	}
	managers := map[string]bool{}
	for _, field := range grant.GetManagedFields() {
		managers[field.Manager] = true
	}
	for _, manager := range []string{"ngd-ngg-prc-spec", "ngd-ngg-prc-status"} {
		if !managers[manager] {
			t.Fatalf("NGG managedFields does not contain %s: %#v", manager, managers)
		}
	}
}

func waitForAllocateCount(t *testing.T, ctx context.Context, mu *sync.Mutex, exchanges *[]controller.AlgorithmExchange, want int) {
	t.Helper()
	if err := common.Eventually(ctx, 10*time.Millisecond, func(context.Context) (bool, error) {
		return allocateCount(snapshotExchanges(mu, exchanges)) >= want, nil
	}); err != nil {
		t.Fatalf("wait Algorithm allocate calls >= %d: %v", want, err)
	}
}

func snapshotExchanges(mu *sync.Mutex, exchanges *[]controller.AlgorithmExchange) []controller.AlgorithmExchange {
	mu.Lock()
	defer mu.Unlock()
	return append([]controller.AlgorithmExchange(nil), (*exchanges)...)
}

func allocateCount(exchanges []controller.AlgorithmExchange) int {
	count := 0
	for _, exchange := range exchanges {
		if exchange.Method == "POST" && exchange.Path == "/api/v1/allocate" {
			count++
		}
	}
	return count
}

func exchangeEvidence(exchanges []controller.AlgorithmExchange) []map[string]any {
	result := make([]map[string]any, 0, len(exchanges))
	for _, exchange := range exchanges {
		if !strings.Contains(exchange.Path, "/api/v1/allocate") {
			continue
		}
		item := map[string]any{"method": exchange.Method, "path": exchange.Path, "statusCode": exchange.StatusCode}
		var request, response any
		_ = json.Unmarshal(exchange.Request, &request)
		_ = json.Unmarshal(exchange.Response, &response)
		item["request"], item["response"] = request, response
		result = append(result, item)
	}
	return result
}

func elapsedMillis(start, end time.Time) float64 {
	return float64(end.Sub(start).Microseconds()) / 1000
}

func currentGroupDirectory(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve group directory")
	}
	return filepath.Dir(source)
}
