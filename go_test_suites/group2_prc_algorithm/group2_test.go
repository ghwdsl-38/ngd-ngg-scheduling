package group2_prc_algorithm_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	algorithm "demo.ngg/algorithm-server/algorithm"
	"demo.ngg/go-test-suites/common"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"scheduling.demo.ngg.io/prc/pkg/controller"
)

func TestGroup2_PRCClientCallsCompleteAlgorithm(t *testing.T) {
	groupDirectory := groupDirectory(t)
	fixture, err := common.GenerateFixture(1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := common.WriteFixtureInput(filepath.Join(groupDirectory, "testdata", "input"), fixture, true); err != nil {
		t.Fatal(err)
	}
	runDirectory := common.NewRunDirectory(t, groupDirectory)
	ctx := context.Background()

	staticID, staticBody, err := controller.BuildStaticSnapshot("mock-1000-node-cluster", fixture.Nodes, nil)
	if err != nil {
		t.Fatalf("build static snapshot: %v", err)
	}
	stateID, capturedAt, schedulerState, err := controller.BuildSchedulerState(fixture.Nodes, fixture.Pods)
	if err != nil {
		t.Fatalf("build scheduler state: %v", err)
	}
	spec, _, _ := unstructured.NestedMap(fixture.Demand.Object, "spec")
	request := map[string]any{
		"requestId": "group2-request-1000", "taskUID": "task-uid-1000", "ngdUID": "ngd-uid-1000", "ngdGeneration": int64(1),
		"requestMode": "resourcePool", "nodeStaticSnapshotId": staticID,
		"schedulerStateSnapshotId": stateID, "schedulerStateCapturedAt": capturedAt,
		"schedulerState": schedulerState, "ngd": spec, "debugTrace": true,
	}

	mock, err := common.NewMockPrometheus("go-test-prometheus-token", fixture.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	if err := mock.AssertAuthenticationBehavior(); err != nil {
		t.Fatalf("Mock Prometheus authentication: %v", err)
	}
	worker := common.PythonWorkerConfig(filepath.Join(runDirectory, "actual"))
	app, err := algorithm.NewApplication(algorithm.Config{
		BootID: "group2-algorithm", PrometheusURL: mock.URL(), PrometheusBearerToken: "go-test-prometheus-token",
		PrometheusClient: mock.Client(), DisableBackgroundMetrics: true,
		PythonExecutable: worker.Executable, PythonModule: worker.Module, PythonPath: worker.PythonPath, WorkerEvidenceDir: worker.EvidenceDir,
	})
	if err != nil {
		t.Fatalf("start Algorithm Application: %v", err)
	}
	defer func() {
		if err := app.Close(); err != nil {
			t.Errorf("close Algorithm: %v", err)
		}
	}()
	if err := app.RefreshMetrics(ctx); err != nil {
		t.Fatalf("preload Prometheus metrics: %v", err)
	}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	exchanges := []controller.AlgorithmExchange{}
	client := controller.AlgorithmClient{BaseURL: server.URL, Client: server.Client(), Recorder: func(exchange controller.AlgorithmExchange) { exchanges = append(exchanges, exchange) }}

	started := time.Now()
	ack, err := client.PutStatic(ctx, staticID, staticBody)
	if err != nil {
		t.Fatalf("PUT static snapshot: %v", err)
	}
	response, err := client.Calculate(ctx, request, 10*time.Second)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("POST allocate: %v", err)
	}
	common.WriteTiming(t, runDirectory, "PRC PUT static snapshot -> PRC parses Allocate response", elapsed)

	if ack.AcceptedSnapshot != staticID {
		t.Fatalf("accepted snapshot=%s, want %s", ack.AcceptedSnapshot, staticID)
	}
	if response.NodeStaticSnapshotID != staticID || response.SchedulerStateSnapshotID != stateID {
		t.Fatalf("response snapshot identity mismatch")
	}
	if len(response.PipelineTrace) != 3 {
		t.Fatalf("pipelineTrace length=%d, want 3", len(response.PipelineTrace))
	}
	if len(response.CandidateNodeGroups) == 0 || len(response.CandidateNodeGroups) > 3 {
		t.Fatalf("candidate groups=%d, want 1..3", len(response.CandidateNodeGroups))
	}
	if len(mock.Requests()) < 14 {
		t.Fatalf("Prometheus requests=%d, want at least 14", len(mock.Requests()))
	}

	actualDirectory := filepath.Join(runDirectory, "actual")
	if err := common.WriteJSON(filepath.Join(actualDirectory, "node-static-snapshot.json"), staticBody); err != nil {
		t.Fatal(err)
	}
	if err := common.WriteJSON(filepath.Join(actualDirectory, "scheduler-state.json"), schedulerState); err != nil {
		t.Fatal(err)
	}
	if err := common.WriteJSON(filepath.Join(actualDirectory, "prometheus-requests.jsonl"), mock.Requests()); err != nil {
		t.Fatal(err)
	}
	if err := common.WriteJSON(filepath.Join(actualDirectory, "prc-algorithm-http.json"), exchangeEvidence(exchanges)); err != nil {
		t.Fatal(err)
	}
	common.CompareGolden(t, filepath.Join(groupDirectory, "testdata", "expected", "static-ack.json"), filepath.Join(actualDirectory, "static-ack.json"), filepath.Join(runDirectory, "comparison", "static-ack-diff.txt"), ack)
	common.CompareGolden(t, filepath.Join(groupDirectory, "testdata", "expected", "algorithm-response.json"), filepath.Join(actualDirectory, "algorithm-response.json"), filepath.Join(runDirectory, "comparison", "algorithm-response-diff.txt"), response)
}

func exchangeEvidence(exchanges []controller.AlgorithmExchange) []map[string]any {
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

func groupDirectory(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve group directory")
	}
	return filepath.Dir(source)
}
