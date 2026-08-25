package common

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

type metricCatalogue struct {
	Version   string `json:"version"`
	NodeLabel string `json:"nodeLabel"`
	Metrics   []struct {
		Name  string `json:"name"`
		Query string `json:"query"`
	} `json:"metrics"`
}

// PrometheusRequest是Mock在内存中保存的正式HTTP请求证据。
type PrometheusRequest struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	Query         string `json:"query"`
	MetricName    string `json:"metricName,omitempty"`
	Authorization string `json:"authorization"`
	StatusCode    int    `json:"statusCode"`
}

// MockPrometheus模拟Bearer认证、PromQL匹配和Prometheus vector响应。
type MockPrometheus struct {
	server      *httptest.Server
	token       string
	nodeLabel   string
	queryToName map[string]string
	metrics     map[string]map[string]float64
	mu          sync.Mutex
	requests    []PrometheusRequest
}

func NewMockPrometheus(token string, metrics map[string]map[string]float64) (*MockPrometheus, error) {
	raw, err := os.ReadFile(filepath.Join(ProjectRoot(), "algorithm_server", "go", "algorithm", "prometheus_metrics.json"))
	if err != nil {
		return nil, err
	}
	var catalogue metricCatalogue
	if err := json.Unmarshal(raw, &catalogue); err != nil {
		return nil, err
	}
	mock := &MockPrometheus{token: token, nodeLabel: catalogue.NodeLabel, queryToName: map[string]string{}, metrics: metrics}
	for _, definition := range catalogue.Metrics {
		mock.queryToName[definition.Query] = definition.Name
	}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.handle))
	return mock, nil
}

func (m *MockPrometheus) URL() string          { return m.server.URL }
func (m *MockPrometheus) Client() *http.Client { return m.server.Client() }
func (m *MockPrometheus) Close()               { m.server.Close() }

func (m *MockPrometheus) Requests() []PrometheusRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]PrometheusRequest(nil), m.requests...)
}

func (m *MockPrometheus) record(request PrometheusRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
}

func (m *MockPrometheus) handle(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query().Get("query")
	record := PrometheusRequest{Method: request.Method, Path: request.URL.Path, Query: query, Authorization: request.Header.Get("Authorization")}
	writeError := func(status int, message string) {
		record.StatusCode = status
		m.record(record)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_ = json.NewEncoder(writer).Encode(map[string]any{"status": "error", "error": message})
	}
	if request.Method != http.MethodGet || request.URL.Path != "/api/v1/query" {
		writeError(http.StatusNotFound, "unknown endpoint")
		return
	}
	if request.Header.Get("Authorization") == "" {
		writeError(http.StatusUnauthorized, "missing bearer token")
		return
	}
	if request.Header.Get("Authorization") != "Bearer "+m.token {
		writeError(http.StatusForbidden, "invalid bearer token")
		return
	}
	metricName, found := m.queryToName[query]
	if !found {
		writeError(http.StatusUnprocessableEntity, "PromQL is not configured")
		return
	}
	record.MetricName = metricName
	record.StatusCode = http.StatusOK
	m.record(record)
	names := make([]string, 0, len(m.metrics))
	for name := range m.metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	results := make([]any, 0, len(names))
	for _, name := range names {
		value, exists := m.metrics[name][metricName]
		if !exists {
			continue
		}
		results = append(results, map[string]any{
			"metric": map[string]string{m.nodeLabel: name},
			"value":  []any{float64(time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC).Unix()), strconv.FormatFloat(value, 'g', -1, 64)},
		})
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": results}})
}

// AssertAuthenticationBehavior独立验证401、403和422，不计入主链路时间。
func (m *MockPrometheus) AssertAuthenticationBehavior() error {
	endpoint := m.URL() + "/api/v1/query?" + url.Values{"query": {"unknown"}}.Encode()
	request, _ := http.NewRequest(http.MethodGet, endpoint, nil)
	response, err := m.Client().Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("missing token status=%d, want 401", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodGet, endpoint, nil)
	request.Header.Set("Authorization", "Bearer wrong")
	response, err = m.Client().Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		return fmt.Errorf("wrong token status=%d, want 403", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodGet, endpoint, nil)
	request.Header.Set("Authorization", "Bearer "+m.token)
	response, err = m.Client().Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		return fmt.Errorf("unknown query status=%d, want 422", response.StatusCode)
	}
	return nil
}
