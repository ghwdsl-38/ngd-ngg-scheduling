// types.go 定义 Go HTTP 层、缓存层和 Python Worker 之间共享的内部协议结构。
package algorithm

import "encoding/json"

type staticSnapshot struct {
	// SnapshotID 是内容 Hash，不使用进程内自增版本号。
	SnapshotID      string           `json:"snapshotId"`
	ClusterID       string           `json:"clusterId"`
	TopologyVersion string           `json:"topologyVersion"`
	Nodes           []map[string]any `json:"nodes"`
}

type metricSnapshot struct {
	// Nodes 以 Kubernetes Node 名称为键，保存 CPU、内存和网络指标。
	SnapshotID       string                        `json:"snapshotId"`
	CapturedAt       string                        `json:"capturedAt"`
	CapturedUnix     float64                       `json:"capturedAtUnix"`
	CatalogueVersion string                        `json:"catalogueVersion,omitempty"`
	Units            map[string]string             `json:"units,omitempty"`
	Coverage         map[string]int                `json:"coverage,omitempty"`
	Nodes            map[string]map[string]float64 `json:"nodes"`
}

type workerPayload struct {
	// 每次调用携带完整静态、动态和指标上下文，Worker 不自行查缓存。
	Request         map[string]any  `json:"request"`
	StaticSnapshot  staticSnapshot  `json:"staticSnapshot"`
	MetricSnapshot  *metricSnapshot `json:"metricSnapshot,omitempty"`
	MetricsDegraded bool            `json:"metricsDegraded"`
	Warnings        []string        `json:"warnings"`
}

type workerResult struct {
	CandidateNodeGroups []map[string]any `json:"candidateNodeGroups"`
	// PipelineTrace 仅在调用方显式 debugTrace=true 时由 Python 返回。
	PipelineTrace []map[string]any `json:"pipelineTrace,omitempty"`
}

type apiError struct {
	RequestID string `json:"requestId"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Status    int    `json:"-"`
}

func (e *apiError) Error() string { return e.Message }

type workerEnvelope struct {
	// ID 将一行请求与一行响应对应起来；Result 与 Error 二选一。
	ID      string        `json:"id"`
	Payload workerPayload `json:"payload"`
	Result  workerResult  `json:"result"`
	Error   *workerError  `json:"error"`
}

type workerError struct {
	RequestID string `json:"requestId"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Status    int    `json:"statusCode"`
}

type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}
