package main

import (
	"fmt"
	"testing"
)

func TestStaticCacheKeepsCurrentAndPrevious(t *testing.T) {
	cache := &staticCache{}
	first := snapshotBody(1)
	firstID, _ := canonicalHash(first)
	if _, err := cache.put(firstID, first); err != nil {
		t.Fatal(err)
	}
	second := snapshotBody(2)
	secondID, _ := canonicalHash(second)
	if _, err := cache.put(secondID, second); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.get(firstID); !ok {
		t.Fatal("previous snapshot not retained")
	}
	status := cache.status()
	if status["currentSnapshotId"] != secondID || status["previousSnapshotId"] != firstID {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestStaticCacheAcceptsOneThousandNodes(t *testing.T) {
	body := snapshotBody(1000)
	id, err := canonicalHash(body)
	if err != nil {
		t.Fatal(err)
	}
	cache := &staticCache{}
	snapshot, err := cache.put(id, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Nodes) != 1000 {
		t.Fatalf("got %d Nodes", len(snapshot.Nodes))
	}
}

func snapshotBody(count int) map[string]any {
	nodes := make([]any, 0, count)
	for index := 0; index < count; index++ {
		nodes = append(nodes, map[string]any{"nodeName": fmt.Sprintf("worker-%04d", index), "nodeUID": fmt.Sprintf("uid-%04d", index), "allocatable": map[string]any{"cpu": "64", "memory": "256Gi"}, "labels": map[string]any{"demo.ngg/worker": "true"}, "topology": map[string]any{"coreSwitchId": "core-1", "leafSwitchId": fmt.Sprintf("leaf-%03d", index/10), "bandwidthGbps": 25, "latencyMillis": 1}})
	}
	return map[string]any{"clusterId": "test", "topologyVersion": "v1", "nodes": nodes}
}
