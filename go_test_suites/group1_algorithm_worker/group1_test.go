package group1_algorithm_worker_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	algorithm "demo.ngg/algorithm-server/algorithm"
	"demo.ngg/go-test-suites/common"
)

func TestGroup1_GoAlgorithmCallsPythonWorker(t *testing.T) {
	groupDirectory := currentDirectory(t)
	fixture, err := common.GenerateFixture(1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := common.WriteFixtureInput(filepath.Join(groupDirectory, "testdata", "input"), fixture, false); err != nil {
		t.Fatal(err)
	}
	runDirectory := common.NewRunDirectory(t, groupDirectory)
	workerConfig := common.PythonWorkerConfig(filepath.Join(runDirectory, "actual"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker, err := algorithm.NewPythonWorker(ctx, workerConfig)
	if err != nil {
		t.Fatalf("start Python Worker: %v", err)
	}
	defer func() {
		if err := worker.Close(); err != nil {
			t.Errorf("close Python Worker: %v", err)
		}
	}()

	// 通用展示对象到正式Worker协议的转换不是JSONL调用的一部分，必须在计时前完成。
	prepared, err := algorithm.PreparePythonRequest(fixture.WorkerPayload)
	if err != nil {
		t.Fatalf("prepare Python request: %v", err)
	}
	started := time.Now()
	parsed, err := worker.CalculatePrepared(ctx, prepared)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Go calls Python Worker: %v", err)
	}
	common.WriteTiming(t, runDirectory, "Go writes prepared Worker JSONL request -> Go parses Python JSONL response", elapsed)
	// 展示map转换和Golden比较发生在计时结束后。
	result, err := parsed.AsMap()
	if err != nil {
		t.Fatalf("convert Python result: %v", err)
	}
	assertPipeline(t, result)
	common.CompareGolden(t,
		filepath.Join(groupDirectory, "testdata", "expected", "worker-result.json"),
		filepath.Join(runDirectory, "actual", "worker-result.json"),
		filepath.Join(runDirectory, "comparison", "diff.txt"), result,
	)
}

func assertPipeline(t *testing.T, result map[string]any) {
	t.Helper()
	trace, ok := result["pipelineTrace"].([]any)
	if !ok || len(trace) != 3 {
		t.Fatalf("pipelineTrace length=%d, want 3", len(trace))
	}
	want := []string{"requirement", "topology", "loadbalance"}
	for index, raw := range trace {
		item, ok := raw.(map[string]any)
		if !ok || fmt.Sprint(item["algorithm"]) != want[index] {
			t.Fatalf("pipelineTrace[%d]=%v, want %s", index, raw, want[index])
		}
	}
	groups, ok := result["candidateNodeGroups"].([]any)
	if !ok || len(groups) == 0 || len(groups) > 3 {
		t.Fatalf("candidateNodeGroups count=%d, want 1..3", len(groups))
	}
	for index, raw := range groups {
		group := raw.(map[string]any)
		if int(group["rank"].(float64)) != index+1 {
			t.Fatalf("rank[%d]=%v", index, group["rank"])
		}
		if nodes, ok := group["nodes"].([]any); !ok || len(nodes) == 0 {
			t.Fatalf("group[%d] has no concrete nodes", index)
		}
	}
}

func currentDirectory(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve group directory")
	}
	return filepath.Dir(source)
}

func TestMain(main *testing.M) { os.Exit(main.Run()) }
