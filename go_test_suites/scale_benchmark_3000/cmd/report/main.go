package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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
	if err := writeGroup2Group4Comparison(reportDirectory, all); err != nil {
		fail("Group2/Group4 containment check: %v", err)
	}
	fmt.Printf("aggregate report: %s\n", reportDirectory)
}

// writeGroup2Group4Comparison用30次Mean验证完整链路没有反常地快于其Algorithm HTTP子区间。
// 单次样本可能受调度抖动影响，因此这里只对整组统计结果进行包含关系校验。
func writeGroup2Group4Comparison(reportDirectory string, all []scale.Statistics) error {
	group2 := map[int]scale.Statistics{}
	group4 := map[int]scale.Statistics{}
	for _, item := range all {
		switch item.Group {
		case "Group2-PRC-Algorithm":
			group2[item.SelectedNodes] = item
		case "Group4-Full-RealAlgorithm":
			group4[item.SelectedNodes] = item
		}
	}
	targets := make([]int, 0, len(group2))
	for target := range group2 {
		targets = append(targets, target)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(targets)))

	var builder strings.Builder
	builder.WriteString("## Group2与Group4包含关系校验\n\n")
	builder.WriteString("相同3000 Node输入和选择规模下，Group4完整链路的30次Mean应大于Group2 Algorithm HTTP子区间。\n\n")
	builder.WriteString("| 选择Node | Group2 Mean(ms) | Group4 Mean(ms) | 增量(ms) | 倍数 | 结论 |\n")
	builder.WriteString("| ---: | ---: | ---: | ---: | ---: | --- |\n")
	failed := false
	for _, target := range targets {
		left := group2[target]
		right, found := group4[target]
		if !found {
			return fmt.Errorf("missing Group4 target=%d", target)
		}
		delta := right.MeanMS - left.MeanMS
		ratio := 0.0
		if left.MeanMS > 0 {
			ratio = right.MeanMS / left.MeanMS
		}
		verdict := "PASS"
		if delta <= 0 {
			verdict = "FAIL"
			failed = true
		}
		fmt.Fprintf(&builder, "| %d | %.3f | %.3f | %.3f | %.2fx | %s |\n", target, left.MeanMS, right.MeanMS, delta, ratio, verdict)
	}
	content := builder.String()
	if err := os.WriteFile(filepath.Join(reportDirectory, "group2-vs-group4.md"), []byte(content), 0o644); err != nil {
		return err
	}
	summary, err := os.OpenFile(filepath.Join(reportDirectory, "summary.md"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprint(summary, "\n"+content); err != nil {
		_ = summary.Close()
		return err
	}
	if err := summary.Close(); err != nil {
		return err
	}
	if failed {
		return fmt.Errorf("Group4 Mean must be greater than Group2 Mean for every target")
	}
	return nil
}

func fail(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", arguments...)
	os.Exit(1)
}
