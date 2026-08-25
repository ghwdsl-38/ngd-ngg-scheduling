package common

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type Sample struct {
	Iteration     int     `json:"iteration"`
	SelectedNodes int     `json:"selectedNodes"`
	ElapsedMS     float64 `json:"elapsedMs"`
	Success       bool    `json:"success"`
}

type Statistics struct {
	Group         string  `json:"group"`
	Boundary      string  `json:"boundary"`
	StaticNodes   int     `json:"staticNodes"`
	SelectedNodes int     `json:"selectedNodes"`
	Samples       int     `json:"samples"`
	Successes     int     `json:"successes"`
	SuccessRate   float64 `json:"successRate"`
	MeanMS        float64 `json:"meanMs"`
	P50MS         float64 `json:"p50Ms"`
	P95MS         float64 `json:"p95Ms"`
	MinMS         float64 `json:"minMs"`
	MaxMS         float64 `json:"maxMs"`
	StdDevMS      float64 `json:"stdDevMs"`
}

func DurationMS(duration time.Duration) float64 { return float64(duration.Nanoseconds()) / 1e6 }

func Summarize(group, boundary string, target int, samples []Sample) Statistics {
	values := make([]float64, 0, len(samples))
	successes := 0
	for _, sample := range samples {
		if sample.Success {
			successes++
			values = append(values, sample.ElapsedMS)
		}
	}
	sort.Float64s(values)
	stats := Statistics{Group: group, Boundary: boundary, StaticNodes: NodeCount, SelectedNodes: target, Samples: len(samples), Successes: successes}
	if len(samples) > 0 {
		stats.SuccessRate = float64(successes) / float64(len(samples)) * 100
	}
	if len(values) == 0 {
		return stats
	}
	var total float64
	for _, value := range values {
		total += value
	}
	stats.MeanMS = total / float64(len(values))
	stats.P50MS = nearestRank(values, 0.50)
	stats.P95MS = nearestRank(values, 0.95)
	stats.MinMS, stats.MaxMS = values[0], values[len(values)-1]
	var variance float64
	for _, value := range values {
		delta := value - stats.MeanMS
		variance += delta * delta
	}
	stats.StdDevMS = math.Sqrt(variance / float64(len(values)))
	return stats
}

func nearestRank(sorted []float64, percentile float64) float64 {
	index := int(math.Ceil(percentile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func RunID() string {
	if value := strings.TrimSpace(os.Getenv("BENCHMARK_RUN_ID")); value != "" {
		return strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(value)
	}
	return time.Now().Format("20060102-150405.000000000")
}

func NewRunDirectory(t *testing.T, groupDirectory string) string {
	t.Helper()
	path := filepath.Join(groupDirectory, "results", RunID())
	for _, child := range []string{"samples", "evidence"} {
		if err := os.MkdirAll(filepath.Join(path, child), 0o755); err != nil {
			t.Fatalf("create benchmark directory: %v", err)
		}
	}
	return path
}

func WriteGroupResults(t *testing.T, runDirectory string, byTarget map[int][]Sample, statistics []Statistics) {
	t.Helper()
	targets := make([]int, 0, len(byTarget))
	for target := range byTarget {
		targets = append(targets, target)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(targets)))
	for _, target := range targets {
		path := filepath.Join(runDirectory, "samples", fmt.Sprintf("select-%d.csv", target))
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		writer := csv.NewWriter(file)
		_ = writer.Write([]string{"iteration", "selectedNodes", "elapsedMs", "success"})
		for _, sample := range byTarget[target] {
			_ = writer.Write([]string{strconv.Itoa(sample.Iteration), strconv.Itoa(sample.SelectedNodes), fmt.Sprintf("%.3f", sample.ElapsedMS), strconv.FormatBool(sample.Success)})
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSON(filepath.Join(runDirectory, "statistics.json"), statistics); err != nil {
		t.Fatal(err)
	}
	if err := writeStatisticsCSV(filepath.Join(runDirectory, "statistics.csv"), statistics); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDirectory, "summary.md"), []byte(statisticsMarkdown(statistics)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeStatisticsCSV(path string, statistics []Statistics) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	_ = writer.Write([]string{"group", "boundary", "staticNodes", "selectedNodes", "samples", "successes", "successRate", "meanMs", "p50Ms", "p95Ms", "minMs", "maxMs", "stdDevMs"})
	for _, item := range statistics {
		_ = writer.Write([]string{item.Group, item.Boundary, strconv.Itoa(item.StaticNodes), strconv.Itoa(item.SelectedNodes), strconv.Itoa(item.Samples), strconv.Itoa(item.Successes), fmt.Sprintf("%.3f", item.SuccessRate), fmt.Sprintf("%.3f", item.MeanMS), fmt.Sprintf("%.3f", item.P50MS), fmt.Sprintf("%.3f", item.P95MS), fmt.Sprintf("%.3f", item.MinMS), fmt.Sprintf("%.3f", item.MaxMS), fmt.Sprintf("%.3f", item.StdDevMS)})
	}
	return writer.Error()
}

func statisticsMarkdown(statistics []Statistics) string {
	var builder strings.Builder
	builder.WriteString("| 测试组 | 静态Node | 选择Node | 次数 | 成功率 | Mean(ms) | P50(ms) | P95(ms) | Min(ms) | Max(ms) | StdDev(ms) |\n")
	builder.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, item := range statistics {
		fmt.Fprintf(&builder, "| %s | %d | %d | %d | %.1f%% | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f |\n", item.Group, item.StaticNodes, item.SelectedNodes, item.Samples, item.SuccessRate, item.MeanMS, item.P50MS, item.P95MS, item.MinMS, item.MaxMS, item.StdDevMS)
	}
	return builder.String()
}

func WriteAggregateReport(reportDirectory string, statistics []Statistics, environment map[string]any) error {
	if err := os.MkdirAll(reportDirectory, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(reportDirectory, "statistics.json"), statistics); err != nil {
		return err
	}
	if err := writeStatisticsCSV(filepath.Join(reportDirectory, "summary.csv"), statistics); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(reportDirectory, "summary.md"), []byte(statisticsMarkdown(statistics)), 0o644); err != nil {
		return err
	}
	return writeJSON(filepath.Join(reportDirectory, "environment.json"), environment)
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
