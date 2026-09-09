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
	covered, expected, applicable := bondInterfaceCoverage(candidates, []lldpNeighbor{{
		LocalInterface: "eno1", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:55",
	}})
	if !applicable || covered != 1 || expected != 2 {
		t.Fatalf("unexpected partial coverage: covered=%d expected=%d applicable=%t", covered, expected, applicable)
	}
	covered, expected, applicable = bondInterfaceCoverage(candidates, []lldpNeighbor{
		{LocalInterface: "eno1", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:55"},
		{LocalInterface: "eno2", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:66"},
	})
	if !applicable || covered != 2 || expected != 2 {
		t.Fatalf("unexpected complete coverage: covered=%d expected=%d applicable=%t", covered, expected, applicable)
	}
	if _, _, applicable = bondInterfaceCoverage(map[int]interfaceSelection{2: {Name: "eth0", Kind: "physical"}}, []lldpNeighbor{{
		LocalInterface: "eth0", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:55",
	}}); applicable {
		t.Fatal("standalone physical interface must finish by idle/max timeout, not bond coverage")
	}
}

func TestBondLeafCompletionRequiresDistinctChassis(t *testing.T) {
	candidates := map[int]interfaceSelection{
		5: {Name: "eno1", Index: 5, Kind: "bond-slave", BondName: "bond0", BondMode: "802.3ad", BondSlaves: []string{"eno1", "eno2"}},
		6: {Name: "eno2", Index: 6, Kind: "bond-slave", BondName: "bond0", BondMode: "802.3ad", BondSlaves: []string{"eno1", "eno2"}},
	}
	sameLeaf := []lldpNeighbor{
		{LocalInterface: "eno1", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:55"},
		{LocalInterface: "eno2", ChassisIDSubtype: "mac-address", ChassisID: "00-11-22-33-44-55"},
	}
	if got := distinctLeafCount(sameLeaf); got != 1 {
		t.Fatalf("same chassis through two links counted as %d Leaves, want 1", got)
	}
	if bondLeafCollectionComplete(candidates, sameLeaf) {
		t.Fatal("two interfaces connected to the same Leaf must not complete dual-Leaf collection")
	}
	if err := validateBondLeafCollection(candidates, sameLeaf, 65*time.Second, []string{"eno1", "eno2"}); err == nil || !strings.Contains(err.Error(), "require 2") {
		t.Fatalf("incomplete dual-Leaf result was not rejected: %v", err)
	}

	differentLeaves := append([]lldpNeighbor(nil), sameLeaf...)
	differentLeaves[1].ChassisID = "00:11:22:33:44:66"
	if got := distinctLeafCount(differentLeaves); got != 2 {
		t.Fatalf("different chassis counted as %d Leaves, want 2", got)
	}
	if !bondLeafCollectionComplete(candidates, differentLeaves) {
		t.Fatal("two selected interfaces with different Leaf chassis must complete collection")
	}
	if err := validateBondLeafCollection(candidates, differentLeaves, 65*time.Second, []string{"eno1", "eno2"}); err != nil {
		t.Fatalf("complete dual-Leaf result was rejected: %v", err)
	}
}

func TestActiveBackupCompletesWithOneActiveLeaf(t *testing.T) {
	candidates := map[int]interfaceSelection{
		6: {Name: "eno2", Index: 6, Kind: "bond-slave", BondName: "bond0", BondMode: "active-backup", BondSlaves: []string{"eno1", "eno2"}, Active: true},
	}
	neighbors := []lldpNeighbor{{LocalInterface: "eno2", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:66"}}
	target, applies := requiredDistinctBondLeaves(candidates)
	if !applies || target != 1 || !bondLeafCollectionComplete(candidates, neighbors) {
		t.Fatalf("unexpected active-backup policy: target=%d applies=%t complete=%t", target, applies, bondLeafCollectionComplete(candidates, neighbors))
	}
}

func TestSelectedInterfaceCoverageRequiresEveryInterface(t *testing.T) {
	candidates := map[int]interfaceSelection{
		5: {Name: "eno1"},
		6: {Name: "eno2"},
	}
	partial := []lldpNeighbor{{LocalInterface: "eno1", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:55"}}
	covered, expected := selectedInterfaceCoverage(candidates, partial)
	if covered != 1 || expected != 2 {
		t.Fatalf("unexpected partial explicit coverage: covered=%d expected=%d", covered, expected)
	}
	complete := append(partial,
		lldpNeighbor{LocalInterface: "eno2", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:66"},
		lldpNeighbor{LocalInterface: "eno2", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:77", LooksLikeLocalHost: true},
	)
	covered, expected = selectedInterfaceCoverage(candidates, complete)
	if covered != 2 || expected != 2 {
		t.Fatalf("unexpected complete explicit coverage: covered=%d expected=%d", covered, expected)
	}
}

func TestBuildLeafLinksAllowsAllExplicitLeafChassis(t *testing.T) {
	selections := map[string]interfaceSelection{
		"eno1": {Name: "eno1"},
		"eno2": {Name: "eno2"},
	}
	links, err := buildLeafLinks([]lldpNeighbor{
		{LocalInterface: "eno1", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:55", SystemName: "leaf-a", PortID: "1"},
		{LocalInterface: "eno1", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:66", SystemName: "leaf-b", PortID: "2"},
		{LocalInterface: "eno2", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:77", SystemName: "leaf-c", PortID: "3"},
	}, selections)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 3 {
		t.Fatalf("explicit Leaf inventory was truncated: %#v", links)
	}
}

func TestBuildLeafLinksRejectsAmbiguousSwitchNames(t *testing.T) {
	selections := map[string]interfaceSelection{
		"eno1": {Name: "eno1", BondName: "bond0", BondMode: "802.3ad", Active: true},
		"eno2": {Name: "eno2", BondName: "bond0", BondMode: "802.3ad", Active: true},
	}
	_, err := buildLeafLinks([]lldpNeighbor{
		{LocalInterface: "eno1", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:55", SystemName: "leaf-a", PortID: "1"},
		{LocalInterface: "eno2", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:66", SystemName: "leaf-a", PortID: "2"},
	}, selections)
	if err == nil || !strings.Contains(err.Error(), "two different LLDP chassis") {
		t.Fatalf("unexpected ambiguity result: %v", err)
	}
}

func TestBuildLeafLinksUsesAvailableSystemNameForSameChassis(t *testing.T) {
	selections := map[string]interfaceSelection{
		"eno1": {Name: "eno1", BondName: "bond0", BondMode: "802.3ad", Active: true},
		"eno2": {Name: "eno2", BondName: "bond0", BondMode: "802.3ad", Active: true},
	}
	links, err := buildLeafLinks([]lldpNeighbor{
		{LocalInterface: "eno1", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:55", PortID: "1"},
		{LocalInterface: "eno2", ChassisIDSubtype: "mac-address", ChassisID: "00-11-22-33-44-55", SystemName: "leaf-a", PortID: "2"},
	}, selections)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 || links[0].LeafSwitchID != "leaf-a" || links[1].LeafSwitchID != "leaf-a" {
		t.Fatalf("same Chassis was not resolved to one canonical System Name: %#v", links)
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
