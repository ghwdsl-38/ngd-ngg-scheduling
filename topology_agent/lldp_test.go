package main

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

func TestParseLLDPFrameReadsSwitchDetails(t *testing.T) {
	frame := make([]byte, 14)
	copy(frame[0:6], []byte{0x01, 0x80, 0xc2, 0x00, 0x00, 0x0e})
	copy(frame[6:12], []byte{0x30, 0xb9, 0x30, 0x32, 0x65, 0xfb})
	binary.BigEndian.PutUint16(frame[12:14], ethernetProtocolLLDP)
	frame = append(frame, makeTLV(1, append([]byte{4}, []byte{0x30, 0xb9, 0x30, 0x32, 0x65, 0xfb}...))...)
	frame = append(frame, makeTLV(2, append([]byte{5}, []byte("Ethernet1/1")...))...)
	frame = append(frame, makeTLV(3, []byte{0, 120})...)
	frame = append(frame, makeTLV(4, []byte("node-uplink"))...)
	frame = append(frame, makeTLV(5, []byte("LEAF-SW01"))...)
	frame = append(frame, makeTLV(6, []byte("ZTE switch"))...)
	frame = append(frame, makeTLV(7, []byte{0, 20, 0, 20})...)
	frame = append(frame, makeTLV(8, []byte{5, 1, 10, 130, 16, 104})...)
	frame = append(frame, makeTLV(0, nil)...)

	got, ok := parseLLDPFrame(frame, "ens5f1np1")
	if !ok {
		t.Fatal("expected valid LLDP frame")
	}
	if got.SystemName != "LEAF-SW01" || got.PortID != "Ethernet1/1" || got.TTLSeconds != 120 {
		t.Fatalf("unexpected neighbor: %#v", got)
	}
	if len(got.ManagementAddresses) != 1 || got.ManagementAddresses[0] != "10.130.16.104" {
		t.Fatalf("unexpected management addresses: %#v", got.ManagementAddresses)
	}
}

func TestParseLLDPFrameRejectsMissingMandatoryTLVs(t *testing.T) {
	frame := make([]byte, 14)
	binary.BigEndian.PutUint16(frame[12:14], ethernetProtocolLLDP)
	frame = append(frame, makeTLV(5, []byte("LEAF-SW01"))...)
	frame = append(frame, makeTLV(0, nil)...)
	if _, ok := parseLLDPFrame(frame, "ens5f1np1"); ok {
		t.Fatal("frame without chassis, port and TTL must be rejected")
	}
}

func TestReceiveLLDPFailsClosedWithoutSelectedInterface(t *testing.T) {
	_, err := receiveLLDP(context.Background(), nil, true, time.Second, 100*time.Millisecond, 0)
	if err == nil || !strings.Contains(err.Error(), "no selected interface") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCollectionModeMatchesReferenceCollector(t *testing.T) {
	for _, test := range []struct {
		count int
		idle  time.Duration
		want  string
	}{
		{count: 2, idle: 3 * time.Second, want: "until-limit"},
		{count: 0, idle: 3 * time.Second, want: "until-idle"},
		{count: 0, idle: 0, want: "full-window"},
	} {
		if got := collectionMode(test.count, test.idle); got != test.want {
			t.Fatalf("collectionMode(%d, %s)=%q, want %q", test.count, test.idle, got, test.want)
		}
	}
}

func TestNextReceiveWaitMatchesReferenceIdleBoundary(t *testing.T) {
	now := time.Date(2026, 9, 8, 17, 44, 56, 0, time.UTC)
	deadline := now.Add(40 * time.Second)
	lastNew := now.Add(-500 * time.Millisecond)
	wait, expired := nextReceiveWait(now, deadline, lastNew, time.Second)
	if expired || wait != 500*time.Millisecond {
		t.Fatalf("unexpected active idle wait: wait=%s expired=%t", wait, expired)
	}
	wait, expired = nextReceiveWait(now.Add(600*time.Millisecond), deadline, lastNew, time.Second)
	if !expired || wait != 0 {
		t.Fatalf("expected idle expiration: wait=%s expired=%t", wait, expired)
	}
}

func TestBondCoverageOnlyCompletesForAllBondSlaves(t *testing.T) {
	candidates := map[int]interfaceSelection{
		5: {Name: "eno1", Index: 5, Kind: "bond-slave", BondName: "bond0"},
		6: {Name: "eno2", Index: 6, Kind: "bond-slave", BondName: "bond0"},
	}
	covered, expected, applicable := bondInterfaceCoverage(candidates, []lldpNeighbor{{LocalInterface: "eno1"}})
	if !applicable || covered != 1 || expected != 2 {
		t.Fatalf("unexpected partial coverage: covered=%d expected=%d applicable=%t", covered, expected, applicable)
	}
	covered, expected, applicable = bondInterfaceCoverage(candidates, []lldpNeighbor{{LocalInterface: "eno1"}, {LocalInterface: "eno2"}})
	if !applicable || covered != 2 || expected != 2 {
		t.Fatalf("unexpected complete coverage: covered=%d expected=%d applicable=%t", covered, expected, applicable)
	}
	if _, _, applicable = bondInterfaceCoverage(map[int]interfaceSelection{2: {Name: "eth0", Kind: "physical"}}, []lldpNeighbor{{LocalInterface: "eth0"}}); applicable {
		t.Fatal("standalone physical interface must finish by idle/max timeout, not bond coverage")
	}
}

func TestNeighborIdentityAllowsMultiplePeersOnOneInterface(t *testing.T) {
	left := lldpNeighbor{LocalInterface: "eno1", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:55", PortIDSubtype: "interface-name", PortID: "eth1"}
	right := left
	right.ChassisID = "00:11:22:33:44:66"
	if neighborIdentity(left) == neighborIdentity(right) {
		t.Fatal("different LLDP peers on one interface must have different identities")
	}
}

func TestSameHostNameHandlesFQDN(t *testing.T) {
	if !sameHostName("worker-01.example.com", "worker-01") {
		t.Fatal("expected FQDN and short hostname to match")
	}
	if sameHostName("leaf-01", "worker-01") {
		t.Fatal("switch and worker names must not match")
	}
}

func makeTLV(kind uint16, value []byte) []byte {
	result := make([]byte, 2, 2+len(value))
	binary.BigEndian.PutUint16(result, kind<<9|uint16(len(value)))
	return append(result, value...)
}
