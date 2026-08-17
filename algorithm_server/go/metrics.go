package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type metricsCache struct {
	mu          sync.RWMutex
	baseURL     string
	cpuQuery    string
	memoryQuery string
	nodeLabel   string
	interval    time.Duration
	staleAfter  time.Duration
	client      *http.Client
	current     *metricSnapshot
	previous    *metricSnapshot
	lastError   string
	lastQuery   time.Time
}

func (c *metricsCache) enabled() bool {
	return c.baseURL != "" && (c.cpuQuery != "" || c.memoryQuery != "")
}

func (c *metricsCache) run(ctx context.Context) {
	if !c.enabled() {
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
	values := map[string]map[string]float64{}
	var err error
	if c.cpuQuery != "" {
		err = c.query(ctx, c.cpuQuery, "cpuUsageRatio", values)
	}
	if err == nil && c.memoryQuery != "" {
		err = c.query(ctx, c.memoryQuery, "memoryUsageRatio", values)
	}
	now := time.Now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastQuery = now
	if err != nil {
		c.lastError = err.Error()
		return
	}
	if len(values) == 0 {
		c.lastError = "Prometheus returned no Node metrics"
		return
	}
	id, hashErr := canonicalHash(map[string]any{"nodes": values})
	if hashErr != nil {
		c.lastError = hashErr.Error()
		return
	}
	next := &metricSnapshot{SnapshotID: id, CapturedAt: now.Format(time.RFC3339), CapturedUnix: float64(now.UnixNano()) / 1e9, Nodes: values}
	if c.current != nil && c.current.SnapshotID != id {
		copy := *c.current
		c.previous = &copy
	}
	c.current = next
	c.lastError = ""
}

func (c *metricsCache) query(ctx context.Context, query, field string, destination map[string]map[string]float64) error {
	endpoint := strings.TrimRight(c.baseURL, "/") + "/api/v1/query?" + url.Values{"query": []string{query}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Prometheus HTTP %d", response.StatusCode)
	}
	var payload prometheusResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return err
	}
	if payload.Status != "success" || payload.Data.ResultType != "vector" {
		return fmt.Errorf("Prometheus instant query did not return a vector")
	}
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
		if err != nil {
			continue
		}
		if value < 0 {
			value = 0
		}
		if value > 1 {
			value = 1
		}
		if destination[name] == nil {
			destination[name] = map[string]float64{}
		}
		destination[name][field] = value
	}
	return nil
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
		return nil, true, []string{message}
	}
	age := time.Since(time.Unix(0, int64(c.current.CapturedUnix*1e9)))
	if age > c.staleAfter {
		warning := fmt.Sprintf("Prometheus metric snapshot is stale: ageSeconds=%.1f", age.Seconds())
		if c.lastError != "" {
			warning += "; lastError=" + c.lastError
		}
		copy := *c.current
		return &copy, true, []string{warning}
	}
	copy := *c.current
	return &copy, false, []string{}
}

func (c *metricsCache) status() map[string]any {
	snapshot, degraded, warnings := c.resolve()
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := map[string]any{
		"ready": snapshot != nil, "enabled": c.enabled(), "degraded": degraded,
		"currentSnapshotId": "metrics-disabled", "previousSnapshotId": "",
		"capturedAt": "", "lastQueryAt": "", "nodeCount": 0,
		"lastError": nil, "warnings": warnings,
	}
	if snapshot != nil {
		result["currentSnapshotId"] = snapshot.SnapshotID
		result["capturedAt"] = snapshot.CapturedAt
		result["nodeCount"] = len(snapshot.Nodes)
	}
	if c.previous != nil {
		result["previousSnapshotId"] = c.previous.SnapshotID
	}
	if !c.lastQuery.IsZero() {
		result["lastQueryAt"] = c.lastQuery.Format(time.RFC3339)
	}
	if c.lastError != "" {
		result["lastError"] = c.lastError
	}
	return result
}
