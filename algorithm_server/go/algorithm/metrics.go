// metrics.go 从 Prometheus 拉取 Node CPU、内存和网络指标，并维护带时效性的内存快照。
package algorithm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type metricsCache struct {
	// current 用于计算，previous 用于观测版本变化；查询失败不清空成功快照。
	mu               sync.RWMutex
	baseURL          string
	definitions      []metricDefinition
	catalogueVersion string
	bearerToken      string
	nodeLabel        string
	interval         time.Duration
	staleAfter       time.Duration
	client           *http.Client
	current          *metricSnapshot
	previous         *metricSnapshot
	lastError        string
	lastWarnings     []string
	lastQuery        time.Time

	// 仅保留给旧配置和现有测试构造器兼容；正式进程使用 definitions。
	cpuQuery    string
	memoryQuery string
}

func (c *metricsCache) configuredDefinitions() []metricDefinition {
	if len(c.definitions) > 0 {
		return c.definitions
	}
	definitions := make([]metricDefinition, 0, 2)
	if c.cpuQuery != "" {
		definitions = append(definitions, metricDefinition{Name: "cpuUsageRatio", Query: c.cpuQuery, Unit: "ratio", Normalization: "ratio", Required: true})
	}
	if c.memoryQuery != "" {
		definitions = append(definitions, metricDefinition{Name: "memoryUsageRatio", Query: c.memoryQuery, Unit: "ratio", Normalization: "ratio", Required: true})
	}
	return definitions
}

func (c *metricsCache) enabled() bool {
	return c.baseURL != "" && len(c.configuredDefinitions()) > 0
}

func (c *metricsCache) run(ctx context.Context) {
	if !c.enabled() {
		log.Printf("component=algorithm event=prometheus_refresh_skipped reason=disabled")
		return
	}
	c.refresh(ctx)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refresh(ctx)
		}
	}
}

func (c *metricsCache) refresh(ctx context.Context) {
	started := time.Now()
	values := map[string]map[string]float64{}
	units := map[string]string{}
	coverage := map[string]int{}
	warnings := []string{}
	var fatalError error
	for _, definition := range c.configuredDefinitions() {
		units[definition.Name] = definition.Unit
		count, err := c.query(ctx, definition, values)
		coverage[definition.Name] = count
		if err == nil && count > 0 {
			continue
		}
		if err == nil {
			err = fmt.Errorf("returned no Node series")
		}
		message := fmt.Sprintf("Prometheus metric %s: %v", definition.Name, err)
		if definition.Required {
			fatalError = fmt.Errorf("required %s", message)
			break
		}
		warnings = append(warnings, message)
	}

	now := time.Now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastQuery = now
	c.lastWarnings = append([]string(nil), warnings...)
	// 必需指标失败时保留上一份完整快照；可选网络指标失败只进入降级告警。
	if fatalError != nil {
		c.lastError = fatalError.Error()
		log.Printf("component=algorithm event=prometheus_refresh_completed status=failed metricCount=%d nodeCount=%d warningCount=%d elapsedMs=%.3f error=%q",
			len(coverage), len(values), len(warnings), durationMilliseconds(started), c.lastError)
		return
	}
	if len(values) == 0 {
		c.lastError = "Prometheus returned no Node metrics"
		log.Printf("component=algorithm event=prometheus_refresh_completed status=failed metricCount=%d nodeCount=0 warningCount=%d elapsedMs=%.3f error=%q",
			len(coverage), len(warnings), durationMilliseconds(started), c.lastError)
		return
	}
	id, err := canonicalHash(map[string]any{"catalogueVersion": c.catalogueVersion, "nodes": values})
	if err != nil {
		c.lastError = err.Error()
		log.Printf("component=algorithm event=prometheus_refresh_completed status=failed metricCount=%d nodeCount=%d warningCount=%d elapsedMs=%.3f error=%q",
			len(coverage), len(values), len(warnings), durationMilliseconds(started), c.lastError)
		return
	}
	next := &metricSnapshot{
		SnapshotID: id, CapturedAt: now.Format(time.RFC3339Nano), CapturedUnix: float64(now.UnixNano()) / 1e9,
		CatalogueVersion: c.catalogueVersion, Units: units, Coverage: coverage, Nodes: values,
	}
	if c.current != nil && c.current.SnapshotID != id {
		previous := *c.current
		c.previous = &previous
	}
	c.current = next
	c.lastError = ""
	log.Printf("component=algorithm event=prometheus_refresh_completed status=success metricCount=%d nodeCount=%d warningCount=%d snapshotId=%s elapsedMs=%.3f",
		len(coverage), len(values), len(warnings), shortLogID(id), durationMilliseconds(started))
}

func (c *metricsCache) query(ctx context.Context, definition metricDefinition, destination map[string]map[string]float64) (int, error) {
	endpoint := strings.TrimRight(c.baseURL, "/") + "/api/v1/query?" + url.Values{"query": []string{definition.Query}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "application/json")
	if c.bearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	var payload prometheusResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return 0, err
	}
	if payload.Status != "success" || payload.Data.ResultType != "vector" {
		return 0, fmt.Errorf("instant query did not return a vector")
	}
	seen := map[string]struct{}{}
	samples := map[string]float64{}
	for _, item := range payload.Data.Result {
		name := item.Metric[c.nodeLabel]
		if name == "" || len(item.Value) != 2 {
			continue
		}
		var raw string
		if err := json.Unmarshal(item.Value[1], &raw); err != nil {
			continue
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		if definition.Normalization == "ratio" {
			value = math.Max(0, math.Min(1, value))
		} else {
			value = math.Max(0, value)
		}
		if _, duplicate := seen[name]; duplicate {
			return 0, fmt.Errorf("duplicate Node series %q", name)
		}
		samples[name] = value
		seen[name] = struct{}{}
	}
	for name, value := range samples {
		if destination[name] == nil {
			destination[name] = map[string]float64{}
		}
		destination[name][definition.Name] = value
	}
	return len(seen), nil
}

func (c *metricsCache) resolve() (*metricSnapshot, bool, []string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.enabled() {
		return nil, true, []string{"Prometheus metrics cache is disabled"}
	}
	if c.current == nil {
		message := c.lastError
		if message == "" {
			message = "Prometheus metric snapshot is not ready"
		}
		return nil, true, append([]string{message}, c.lastWarnings...)
	}
	warnings := append([]string(nil), c.lastWarnings...)
	// 可选网络指标缺失只降低覆盖率，不把仍有必需指标的快照整体判为不可用。
	degraded := false
	if c.lastError != "" {
		warnings = append(warnings, "last required metric refresh failed: "+c.lastError)
		degraded = true
	}
	age := time.Since(time.Unix(0, int64(c.current.CapturedUnix*1e9)))
	if age > c.staleAfter {
		warning := fmt.Sprintf("Prometheus metric snapshot is stale: ageSeconds=%.1f", age.Seconds())
		if c.lastError != "" {
			warning += "; lastError=" + c.lastError
		}
		warnings = append(warnings, warning)
		degraded = true
	}
	copy := *c.current
	return &copy, degraded, warnings
}

func (c *metricsCache) status() map[string]any {
	snapshot, degraded, warnings := c.resolve()
	c.mu.RLock()
	defer c.mu.RUnlock()
	metricNames := []string{}
	for _, definition := range c.configuredDefinitions() {
		metricNames = append(metricNames, definition.Name)
	}
	currentSnapshotID := "metrics-disabled"
	if c.enabled() {
		currentSnapshotID = "metrics-unavailable"
	}
	result := map[string]any{
		"ready": snapshot != nil, "enabled": c.enabled(), "degraded": degraded,
		"catalogueVersion": c.catalogueVersion, "metricNames": metricNames,
		"currentSnapshotId": currentSnapshotID, "previousSnapshotId": "",
		"capturedAt": "", "lastQueryAt": "", "nodeCount": 0,
		"coverage": map[string]int{}, "lastError": nil, "warnings": warnings,
	}
	if snapshot != nil {
		result["currentSnapshotId"] = snapshot.SnapshotID
		result["capturedAt"] = snapshot.CapturedAt
		result["nodeCount"] = len(snapshot.Nodes)
		result["coverage"] = snapshot.Coverage
	}
	if c.previous != nil {
		result["previousSnapshotId"] = c.previous.SnapshotID
	}
	if !c.lastQuery.IsZero() {
		result["lastQueryAt"] = c.lastQuery.Format(time.RFC3339Nano)
	}
	if c.lastError != "" {
		result["lastError"] = c.lastError
	}
	return result
}
