package group1_algorithm_worker_test

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	algorithm "demo.ngg/algorithm-server/algorithm"
	base "demo.ngg/go-test-suites/common"
	scale "demo.ngg/go-test-suites/scale_benchmark_3000/common"
)

const groupName = "Group1-Go-Python"

func TestGroup1Scale3000(t *testing.T) {
	fixture, err := scale.GenerateFixture()
	if err != nil {
		t.Fatal(err)
	}
	if err := scale.WriteCanonicalInputs(scale.ScaleRoot(), fixture); err != nil {
		t.Fatal(err)
	}
	targets, samples, warmups := benchmarkParameters(t)
	runDirectory := scale.NewRunDirectory(t, currentDirectory(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker, err := algorithm.NewPythonWorker(ctx, base.PythonWorkerConfig(""))
	if err != nil {
		t.Fatalf("start Python Worker: %v", err)
	}
	defer func() {
		if err := worker.Close(); err != nil {
			t.Errorf("close Python Worker: %v", err)
		}
	}()

	boundary := "Go writes prepared 3000-Node JSONL request -> Go parses Python JSONL response"
	byTarget := map[int][]scale.Sample{}
	statistics := []scale.Statistics{}
	for _, target := range targets {
		for iteration := 1; iteration <= warmups; iteration++ {
			payload := fixture.WorkerPayload(target, fmt.Sprintf("g1-select-%d-warmup-%d", target, iteration))
			prepared, err := algorithm.PreparePythonRequest(payload)
			if err != nil {
				t.Fatalf("target=%d warmup=%d prepare: %v", target, iteration, err)
			}
			parsed, err := worker.CalculatePrepared(ctx, prepared)
			if err != nil {
				t.Fatalf("target=%d warmup=%d: %v", target, iteration, err)
			}
			result, err := parsed.AsMap()
			if err != nil {
				t.Fatalf("target=%d warmup=%d result conversion: %v", target, iteration, err)
			}
			if err := scale.ValidateGenericResult(result, target); err != nil {
				t.Fatalf("target=%d warmup=%d validation: %v", target, iteration, err)
			}
		}
		var evidenceRequest, evidenceResponse map[string]any
		for iteration := 1; iteration <= samples; iteration++ {
			payload := fixture.WorkerPayload(target, fmt.Sprintf("g1-select-%d-sample-%02d", target, iteration))
			// 通用展示对象到正式Worker协议类型的转换不属于生产JSONL调用，
			// 必须在开始计时前完成。
			prepared, err := algorithm.PreparePythonRequest(payload)
			if err != nil {
				t.Fatalf("target=%d sample=%d prepare: %v", target, iteration, err)
			}
			started := time.Now()
			parsed, err := worker.CalculatePrepared(ctx, prepared)
			elapsed := time.Since(started)
			if err != nil {
				t.Fatalf("target=%d sample=%d: %v", target, iteration, err)
			}
			// 展示用map转换、结果校验及证据文件写入均在计时结束后执行。
			result, err := parsed.AsMap()
			if err != nil {
				t.Fatalf("target=%d sample=%d result conversion: %v", target, iteration, err)
			}
			if err := scale.ValidateGenericResult(result, target); err != nil {
				t.Fatalf("target=%d sample=%d validation: %v", target, iteration, err)
			}
			byTarget[target] = append(byTarget[target], scale.Sample{Iteration: iteration, SelectedNodes: target, ElapsedMS: scale.DurationMS(elapsed), Success: true})
			evidenceRequest, evidenceResponse = payload, result
		}
		stats := scale.Summarize(groupName, boundary, target, byTarget[target])
		statistics = append(statistics, stats)
		t.Logf("target=%d mean=%.3fms p50=%.3fms p95=%.3fms", target, stats.MeanMS, stats.P50MS, stats.P95MS)
		evidence := filepath.Join(runDirectory, "evidence", fmt.Sprintf("select-%d", target))
		if err := base.WriteJSON(filepath.Join(evidence, "go-to-python-request.json"), evidenceRequest); err != nil {
			t.Fatal(err)
		}
		if err := base.WriteJSON(filepath.Join(evidence, "python-to-go-response.json"), evidenceResponse); err != nil {
			t.Fatal(err)
		}
	}
	scale.WriteGroupResults(t, runDirectory, byTarget, statistics)
	t.Logf("results=%s", runDirectory)
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
