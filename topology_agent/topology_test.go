package main

import (
	"encoding/binary"
	"testing"
)

func TestResolveThreeLevelTopology(t *testing.T) {
	config := topologyConfig{Version: "v1", LeafSwitches: map[string]leafConfig{"leaf-a": {BorderSwitchID: "border-a", BandwidthGbps: 25, LatencyMillis: 1}}, BorderSwitches: map[string]borderConfig{"border-a": {CoreSwitchID: "core-0"}}}
	result, err := config.resolve("leaf-a")
	if err != nil {
		t.Fatal(err)
	}
	if result.BorderSwitchID != "border-a" || result.CoreSwitchID != "core-0" {
		t.Fatalf("unexpected topology: %#v", result)
	}
}

func TestParseLLDPFrame(t *testing.T) {
	payload := append(tlv(1, append([]byte{7}, []byte("chassis-1")...)), tlv(2, append([]byte{7}, []byte("Ethernet1/1")...))...)
	payload = append(payload, tlv(3, []byte{0, 120})...)
	payload = append(payload, tlv(5, []byte("switch-a"))...)
	payload = append(payload, 0, 0)
	frame := append(make([]byte, 12), 0x88, 0xcc)
	frame = append(frame, payload...)
	neighbor, ok := parseLLDPFrame(frame, "eth0")
	if !ok || neighbor.switchID() != "switch-a" || neighbor.PortID != "Ethernet1/1" {
		t.Fatalf("unexpected neighbor: %#v", neighbor)
	}
}

func TestRestoreOneThousandNodeLabels(t *testing.T) {
	for index := 0; index < 1000; index++ {
		labels := map[string]string{labelPrefix + "leaf-switch": "leaf-a", labelPrefix + "border-switch": "border-a", labelPrefix + "core-switch": "core-0", labelPrefix + "bandwidth-gbps": "25", labelPrefix + "latency-ms": "1", labelPrefix + "topology-version": "v1", labelPrefix + "source": "LLDP"}
		if _, ok := observationFromLabels(labels, map[string]string{}); !ok {
			t.Fatalf("Node %d was not restored", index)
		}
	}
}

func tlv(kind uint16, value []byte) []byte {
	result := make([]byte, 2+len(value))
	binary.BigEndian.PutUint16(result, kind<<9|uint16(len(value)))
	copy(result[2:], value)
	return result
}
