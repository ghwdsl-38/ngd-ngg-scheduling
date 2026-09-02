package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type AlgorithmClient struct {
	BaseURL string
	Client  *http.Client
	// Recorder 是可选的协议观测钩子。生产默认为 nil；验收测试用它保存
	// PRC→Algorithm 的原始请求、响应、状态码和耗时。
	Recorder AlgorithmExchangeRecorder
}

type AlgorithmExchange struct {
	Method     string
	Path       string
	Request    []byte
	Response   []byte
	StatusCode int
	Duration   time.Duration
}

type AlgorithmExchangeRecorder func(AlgorithmExchange)

type StaticAck struct {
	AlgorithmBootID  string `json:"algorithmBootId"`
	AcceptedSnapshot string `json:"acceptedSnapshotId"`
	NodeCount        int    `json:"nodeCount"`
}

// StaticCacheStatus是Algorithm静态缓存状态接口的最小协议。
// PRC独立静态同步器用它识别Algorithm重启和缓存丢失，避免重复传输未变化快照。
type StaticCacheStatus struct {
	AlgorithmBootID  string `json:"algorithmBootId"`
	Ready            bool   `json:"ready"`
	AcceptedSnapshot string `json:"acceptedSnapshotId"`
	NodeCount        int    `json:"nodeCount"`
}

type CandidateNode struct {
	NodeUID   string            `json:"nodeUID"`
	NodeName  string            `json:"nodeName"`
	Score     int64             `json:"score"`
	Resources map[string]string `json:"resources,omitempty"`
	Topology  map[string]any    `json:"topology,omitempty"`
}

type CandidateGroup struct {
	Rank          int64           `json:"rank"`
	GroupID       string          `json:"groupId"`
	TopologyLevel string          `json:"topologyLevel"`
	GroupScore    float64         `json:"groupScore"`
	Nodes         []CandidateNode `json:"nodes"`
}

type AlgorithmResponse struct {
	RequestID                string           `json:"requestId"`
	TaskUID                  string           `json:"taskUID"`
	NGDUID                   string           `json:"ngdUID"`
	NGDGeneration            int64            `json:"ngdGeneration"`
	AlgorithmBootID          string           `json:"algorithmBootId"`
	NodeStaticSnapshotID     string           `json:"nodeStaticSnapshotId"`
	SchedulerStateSnapshotID string           `json:"schedulerStateSnapshotId"`
	MetricSnapshotID         string           `json:"metricSnapshotId"`
	MetricSnapshotCapturedAt string           `json:"metricSnapshotCapturedAt"`
	TopologySnapshotID       string           `json:"topologySnapshotId"`
	Degraded                 bool             `json:"degraded"`
	Warnings                 []string         `json:"warnings"`
	Status                   string           `json:"status"`
	CandidateNodeGroups      []CandidateGroup `json:"candidateNodeGroups"`
	PipelineTrace            []map[string]any `json:"pipelineTrace,omitempty"`
}

func (a AlgorithmClient) putStatic(ctx context.Context, snapshotID string, body any) (StaticAck, error) {
	var result StaticAck
	err := a.do(ctx, http.MethodPut, "/internal/v1/node-static-snapshots/"+url.PathEscape(snapshotID), body, &result)
	return result, err
}

// PutStatic上传PRC生成的Node静态快照。生产Reconcile与独立Go Test共享该入口。
func (a AlgorithmClient) PutStatic(ctx context.Context, snapshotID string, body any) (StaticAck, error) {
	return a.putStatic(ctx, snapshotID, body)
}

func (a AlgorithmClient) staticStatus(ctx context.Context) (StaticCacheStatus, error) {
	var result StaticCacheStatus
	err := a.do(ctx, http.MethodGet, "/internal/v1/node-static-cache/status", nil, &result)
	return result, err
}

// StaticStatus读取Algorithm当前Boot ID和已接收的静态快照身份。
func (a AlgorithmClient) StaticStatus(ctx context.Context) (StaticCacheStatus, error) {
	return a.staticStatus(ctx)
}

func (a AlgorithmClient) calculate(ctx context.Context, body any, timeout time.Duration) (AlgorithmResponse, error) {
	var result AlgorithmResponse
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := a.do(requestCtx, http.MethodPost, "/api/v1/allocate", body, &result)
	return result, err
}

// Calculate发送任务级Node动态状态和完整NGD，并解析Algorithm候选组响应。
func (a AlgorithmClient) Calculate(ctx context.Context, body any, timeout time.Duration) (AlgorithmResponse, error) {
	return a.calculate(ctx, body, timeout)
}

func (a AlgorithmClient) do(ctx context.Context, method, path string, body, result any) error {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal Algorithm request: %w", err)
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, a.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("create Algorithm request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	started := time.Now()
	response, err := a.Client.Do(request)
	if err != nil {
		if a.Recorder != nil {
			a.Recorder(AlgorithmExchange{Method: method, Path: path, Request: raw, Duration: time.Since(started)})
		}
		return fmt.Errorf("call Algorithm: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read Algorithm response: %w", err)
	}
	if a.Recorder != nil {
		a.Recorder(AlgorithmExchange{Method: method, Path: path, Request: raw, Response: responseBody, StatusCode: response.StatusCode, Duration: time.Since(started)})
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Algorithm HTTP %d: %s", response.StatusCode, string(responseBody))
	}
	if err := json.Unmarshal(responseBody, result); err != nil {
		return fmt.Errorf("decode Algorithm response: %w", err)
	}
	return nil
}
