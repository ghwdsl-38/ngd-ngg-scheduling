package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAlgorithmClientDecodesBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"requestId":"r1","code":"MIN_RESOURCES_EXCEEDS_QUOTA","message":"spec.minResources.memory=500Gi exceeds spec.quota.memory=400Gi","retryable":false}`))
	}))
	defer server.Close()

	client := AlgorithmClient{BaseURL: server.URL, Client: server.Client()}
	_, err := client.Calculate(context.Background(), map[string]any{}, time.Second)
	var apiError *AlgorithmAPIError
	if !errors.As(err, &apiError) {
		t.Fatalf("error=%v, want AlgorithmAPIError", err)
	}
	if apiError.Code != "MIN_RESOURCES_EXCEEDS_QUOTA" || apiError.Retryable {
		t.Fatalf("apiError=%+v", apiError)
	}
	if apiError.Message != "spec.minResources.memory=500Gi exceeds spec.quota.memory=400Gi" {
		t.Fatalf("message=%q", apiError.Message)
	}
}
