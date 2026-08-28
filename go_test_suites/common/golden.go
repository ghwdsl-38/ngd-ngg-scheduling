package common

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// NewRunDirectory在当前测试组内部创建独立结果目录。
func NewRunDirectory(t *testing.T, groupDirectory string) string {
	t.Helper()
	runID := time.Now().Format("20060102-150405.000000000")
	path := filepath.Join(groupDirectory, "results", runID)
	for _, child := range []string{"actual", "comparison"} {
		if err := os.MkdirAll(filepath.Join(path, child), 0o755); err != nil {
			t.Fatalf("create result directory: %v", err)
		}
	}
	return path
}

// CompareGolden保存Actual、读取Expected并把比较结论写入本组comparison目录。
func CompareGolden(t *testing.T, expectedPath, actualPath, diffPath string, actual any) {
	t.Helper()
	generic, err := ToGeneric(actual)
	if err != nil {
		t.Fatalf("convert actual to JSON: %v", err)
	}
	normalized := NormalizeForGolden(generic)
	if err := WriteJSON(actualPath, normalized); err != nil {
		t.Fatalf("write actual: %v", err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := WriteJSON(expectedPath, normalized); err != nil {
			t.Fatalf("update golden: %v", err)
		}
	}
	var expected any
	if err := ReadJSON(expectedPath, &expected); err != nil {
		t.Fatalf("read expected %s: %v", expectedPath, err)
	}
	expectedRaw, _ := json.MarshalIndent(NormalizeForGolden(expected), "", "  ")
	actualRaw, _ := json.MarshalIndent(normalized, "", "  ")
	message := "PASS: Expected与Actual一致\n"
	if string(expectedRaw) != string(actualRaw) {
		message = simpleDiff(string(expectedRaw), string(actualRaw))
		_ = os.WriteFile(diffPath, []byte(message), 0o644)
		t.Fatalf("Expected与Actual不一致，详见 %s", diffPath)
	}
	if err := os.WriteFile(diffPath, []byte(message), 0o644); err != nil {
		t.Fatalf("write comparison: %v", err)
	}
}

func simpleDiff(expected, actual string) string {
	left, right := strings.Split(expected, "\n"), strings.Split(actual, "\n")
	limit := len(left)
	if len(right) > limit {
		limit = len(right)
	}
	var builder strings.Builder
	builder.WriteString("Expected与Actual不一致：\n")
	shown := 0
	for index := 0; index < limit && shown < 40; index++ {
		var want, got string
		if index < len(left) {
			want = left[index]
		}
		if index < len(right) {
			got = right[index]
		}
		if want == got {
			continue
		}
		fmt.Fprintf(&builder, "line %d\n- %s\n+ %s\n", index+1, want, got)
		shown++
	}
	return builder.String()
}

// WriteTiming在业务计时结束后记录边界和毫秒值。
func WriteTiming(t *testing.T, runDirectory, boundary string, duration time.Duration) {
	t.Helper()
	elapsedMS := float64(duration.Microseconds()) / 1000
	content := fmt.Sprintf("unit: ms\nboundary: %s\nelapsedMs: %.3f\n", boundary, elapsedMS)
	if err := os.WriteFile(filepath.Join(runDirectory, "timing.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("write timing: %v", err)
	}
	// go test末尾的秒数是包含环境准备/清理的整包耗时；这里显式输出真正的业务边界和毫秒值。
	t.Logf("businessTiming boundary=%q elapsedMs=%.3f", boundary, elapsedMS)
}
