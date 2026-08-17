package main

import "encoding/json"

type staticSnapshot struct {
	SnapshotID      string           `json:"snapshotId"`
	ClusterID       string           `json:"clusterId"`
	TopologyVersion string           `json:"topologyVersion"`
	Nodes           []map[string]any `json:"nodes"`
}

type metricSnapshot struct {
	SnapshotID   string                        `json:"snapshotId"`
	CapturedAt   string                        `json:"capturedAt"`
	CapturedUnix float64                       `json:"capturedAtUnix"`
	Nodes        map[string]map[string]float64 `json:"nodes"`
}

type workerPayload struct {
	Request         map[string]any  `json:"request"`
	StaticSnapshot  staticSnapshot  `json:"staticSnapshot"`
	MetricSnapshot  *metricSnapshot `json:"metricSnapshot,omitempty"`
	MetricsDegraded bool            `json:"metricsDegraded"`
	Warnings        []string        `json:"warnings"`
}

type workerResult struct {
	CandidateNodeGroups []map[string]any `json:"candidateNodeGroups"`
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
