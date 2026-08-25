package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	scale "demo.ngg/go-test-suites/scale_benchmark_3000/common"
)

func main() {
	runID := scale.RunID()
	root := scale.ScaleRoot()
	groups := []string{
		"group1_algorithm_worker",
		"group2_prc_algorithm",
		"group3_prc_ngd_ngg",
		"group4_full_real_algorithm",
	}
	all := []scale.Statistics{}
	for _, group := range groups {
		path := filepath.Join(root, group, "results", runID, "statistics.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			fail("read %s: %v", path, err)
		}
		var items []scale.Statistics
		if err := json.Unmarshal(raw, &items); err != nil {
			fail("decode %s: %v", path, err)
		}
		all = append(all, items...)
	}
	order := map[string]int{"Group1-Go-Python": 1, "Group2-PRC-Algorithm": 2, "Group3-PRC-MockAlgorithm-NGG": 3, "Group4-Full-RealAlgorithm": 4}
	sort.SliceStable(all, func(i, j int) bool {
		if order[all[i].Group] != order[all[j].Group] {
			return order[all[i].Group] < order[all[j].Group]
		}
		return all[i].SelectedNodes > all[j].SelectedNodes
	})
	environment := map[string]any{
		"runId": runID, "generatedAt": time.Now().UTC().Format(time.RFC3339Nano),
		"goVersion": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH,
		"gomaxprocs": runtime.GOMAXPROCS(0), "staticNodes": scale.NodeCount,
		"defaultSamples": scale.DefaultSamples, "defaultWarmups": scale.DefaultWarmups,
		"percentileMethod": "nearest-rank",
	}
	reportDirectory := filepath.Join(root, "reports", runID)
	if err := scale.WriteAggregateReport(reportDirectory, all, environment); err != nil {
		fail("write report: %v", err)
	}
	fmt.Printf("aggregate report: %s\n", reportDirectory)
}

func fail(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", arguments...)
	os.Exit(1)
}
