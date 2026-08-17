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
}

type StaticAck struct {
	AlgorithmBootID  string `json:"algorithmBootId"`
	AcceptedSnapshot string `json:"acceptedSnapshotId"`
	NodeCount        int    `json:"nodeCount"`
}

type CandidateNode struct {
	NodeUID  string `json:"nodeUID"`
	NodeName string `json:"nodeName"`
	Score    int64  `json:"score"`
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
	Status                   string           `json:"status"`
	CandidateNodeGroups      []CandidateGroup `json:"candidateNodeGroups"`
}

func (a AlgorithmClient) putStatic(ctx context.Context, snapshotID string, body any) (StaticAck, error) {
	var result StaticAck
	err := a.do(ctx, http.MethodPut, "/internal/v1/node-static-snapshots/"+url.PathEscape(snapshotID), body, &result)
	return result, err
}

func (a AlgorithmClient) calculate(ctx context.Context, body any, timeout time.Duration) (AlgorithmResponse, error) {
	var result AlgorithmResponse
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := a.do(requestCtx, http.MethodPost, "/api/v1/node-groups/calculate", body, &result)
	return result, err
}

func (a AlgorithmClient) do(ctx context.Context, method, path string, body, result any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal Algorithm request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, method, a.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("create Algorithm request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.Client.Do(request)
	if err != nil {
		return fmt.Errorf("call Algorithm: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read Algorithm response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Algorithm HTTP %d: %s", response.StatusCode, string(responseBody))
	}
	if err := json.Unmarshal(responseBody, result); err != nil {
		return fmt.Errorf("decode Algorithm response: %w", err)
	}
	return nil
}
