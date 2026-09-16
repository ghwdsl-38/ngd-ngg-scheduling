package group2_prc_algorithm_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	algorithm "demo.ngg/algorithm-server/algorithm"
	base "demo.ngg/go-test-suites/common"
	scale "demo.ngg/go-test-suites/scale_benchmark_3000/common"
	"scheduling.demo.ngg.io/prc/pkg/controller"
)

const groupName = "Group2-PRC-Algorithm"

func TestGroup2Scale3000(t *testing.T) {
	fixture, err := scale.GenerateFixture()
	if err != nil {
		t.Fatal(err)
	}
	if err := scale.WriteCanonicalInputs(scale.ScaleRoot(), fixture); err != nil {
		t.Fatal(err)
	}
	targets, samples, warmups := benchmarkParameters(t)
	runDirectory := scale.NewRunDirectory(t, currentDirectory(t))
	ctx := context.Background()
	staticID, staticBody, err := controller.BuildStaticSnapshot(scale.ClusterID, fixture.Nodes)
	if err != nil {
		t.Fatal(err)
	}
	stateID, capturedAt, schedulerState, err := controller.BuildSchedulerState(fixture.Nodes, nil)
	if err != nil {
		t.Fatal(err)
	}
	prometheus, err := base.NewMockPrometheus("benchmark-prometheus-token", fixture.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	defer prometheus.Close()
	worker := base.PythonWorkerConfig("")
	app, err := algorithm.NewApplication(algorithm.Config{
		BootID: "benchmark-group2", PrometheusURL: prometheus.URL(), PrometheusBearerToken: "benchmark-prometheus-token",
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
	if err := app.RefreshMetrics(ctx); err != nil {
		t.Fatalf("preload Prometheus: %v", err)
	}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	client := controller.AlgorithmClient{BaseURL: server.URL, Client: server.Client()}
	ack, err := client.PutStatic(ctx, staticID, staticBody)
	if err != nil || ack.AcceptedSnapshot != staticID {
		t.Fatalf("preload static snapshot: ack=%+v err=%v", ack, err)
	}

	boundary := "PRC POST Allocate with warm static/metrics -> PRC parses response"
	byTarget := map[int][]scale.Sample{}
	statistics := []scale.Statistics{}
	for _, target := range targets {
		for iteration := 1; iteration <= warmups; iteration++ {
			request := allocationRequest(target, fmt.Sprintf("g2-select-%d-warmup-%d", target, iteration), staticID, stateID, capturedAt, schedulerState)
			response, err := client.Calculate(ctx, request, 30*time.Second)
			if err != nil {
				t.Fatalf("target=%d warmup=%d: %v", target, iteration, err)
			}
			if err := scale.ValidateResponse(response, target); err != nil {
				t.Fatalf("target=%d warmup=%d validation: %v", target, iteration, err)
			}
		}
		var evidenceRequest map[string]any
		var evidenceResponse controller.AlgorithmResponse
		for iteration := 1; iteration <= samples; iteration++ {
			request := allocationRequest(target, fmt.Sprintf("g2-select-%d-sample-%02d", target, iteration), staticID, stateID, capturedAt, schedulerState)
			started := time.Now()
			response, err := client.Calculate(ctx, request, 30*time.Second)
			elapsed := time.Since(started)
			if err != nil {
				t.Fatalf("target=%d sample=%d: %v", target, iteration, err)
			}
			if err := scale.ValidateResponse(response, target); err != nil {
				t.Fatalf("target=%d sample=%d validation: %v", target, iteration, err)
			}
			byTarget[target] = append(byTarget[target], scale.Sample{Iteration: iteration, SelectedNodes: target, ElapsedMS: scale.DurationMS(elapsed), Success: true})
			evidenceRequest, evidenceResponse = request, response
		}
		stats := scale.Summarize(groupName, boundary, target, byTarget[target])
		statistics = append(statistics, stats)
		t.Logf("target=%d mean=%.3fms p50=%.3fms p95=%.3fms", target, stats.MeanMS, stats.P50MS, stats.P95MS)
		evidence := filepath.Join(runDirectory, "evidence", fmt.Sprintf("select-%d", target))
		if err := base.WriteJSON(filepath.Join(evidence, "prc-allocation-request.json"), evidenceRequest); err != nil {
			t.Fatal(err)
		}
		if err := base.WriteJSON(filepath.Join(evidence, "algorithm-response.json"), evidenceResponse); err != nil {
			t.Fatal(err)
		}
	}
	scale.WriteGroupResults(t, runDirectory, byTarget, statistics)
	t.Logf("results=%s", runDirectory)
}

func allocationRequest(target int, requestID, staticID, stateID, capturedAt string, schedulerState any) map[string]any {
	return map[string]any{
		"requestId": requestID, "taskUID": requestID + "-task", "ngdUID": requestID + "-ngd", "ngdGeneration": int64(1),
		"requestMode": "resourcePool", "nodeStaticSnapshotId": staticID,
		"schedulerStateSnapshotId": stateID, "schedulerStateCapturedAt": capturedAt,
		"schedulerState": schedulerState, "ngd": scale.DemandSpec(target), "debugTrace": false,
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
