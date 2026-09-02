package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSimulatedLLDPWritesOnlyDirectLeafAsNodeLabel(t *testing.T) {
	var patch map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPatch || request.URL.Path != "/api/v1/nodes/worker-1" {
			http.NotFound(writer, request)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&patch); err != nil {
			t.Fatalf("decode Node patch: %v", err)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	agent := &topologyAgent{
		client:   &kubeClient{baseURL: server.URL, client: server.Client()},
		nodeName: "worker-1",
		mode:     "Simulated",
		listen:   time.Second,
	}
	node := nodeObject{Metadata: nodeMetadata{
		Name: "worker-1",
		Annotations: map[string]string{
			labelPrefix + "simulated-leaf-switch": "HB-HL-DC1-102-C03-44U-LTY-CSQ-LEAF-SW01-ZTE5960X",
			labelPrefix + "local-interface":       "eth0",
			labelPrefix + "remote-port":           "cgei-0/1/1/1",
		},
	}}
	if err := agent.reconcile(context.Background(), node); err != nil {
		t.Fatalf("reconcile simulated LLDP: %v", err)
	}
	metadata := patch["metadata"].(map[string]any)
	labels := metadata["labels"].(map[string]any)
	if len(labels) != 1 || labels[labelPrefix+"leaf-switch"] == "" {
		t.Fatalf("Node labels must contain only direct Leaf: %#v", labels)
	}
	for _, forbidden := range []string{"border-switch", "core-switch", "bandwidth-gbps", "latency-ms"} {
		if _, exists := labels[labelPrefix+forbidden]; exists {
			t.Fatalf("upper topology %s must not be written to Node: %#v", forbidden, labels)
		}
	}
}
