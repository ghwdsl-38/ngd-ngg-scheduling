package main

import (
	"context"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
	"time"

	"demo.ngg/topology-agent/pkg/topologyfacts"
)

func TestBondMasterFramesKeepBothLeaves(t *testing.T) {
	selection := map[string]interfaceSelection{"bond0": {
		Name: "bond0", Kind: "bond-master", BondName: "bond0", BondMode: "802.3ad",
	}}
	var neighbors []lldpNeighbor
	for i, name := range []string{"leaf-a", "leaf-b"} {
		frame := make([]byte, 14)
		binary.BigEndian.PutUint16(frame[12:14], ethernetProtocolLLDP)
		frame = append(frame, makeTLV(1, []byte{4, 0, 1, 2, 3, 4, byte(i + 1)})...)
		frame = append(frame, makeTLV(2, append([]byte{5}, []byte("port-1")...))...)
		frame = append(frame, makeTLV(3, []byte{0, 120})...)
		frame = append(frame, makeTLV(5, []byte(name))...)
		frame = append(frame, makeTLV(0, nil)...)
		neighbor, ok := parseLLDPFrame(frame, "bond0")
		if !ok {
			t.Fatal("valid Bond LLDP frame rejected")
		}
		neighbors = append(neighbors, neighbor)
	}
	if distinctLeafCount(neighbors) != 2 {
		t.Fatal("two chassis collapsed into one")
	}
	build := func(items []lldpNeighbor) (map[string]any, map[string]any) {
		t.Helper()
		links, err := buildLeafLinks(items, selection)
		if err != nil {
			t.Fatal(err)
		}
		observed, labels, annotations, err := topologyfacts.BuildNodeMetadata(observation{Links: links, Source: "LLDP"}, time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
		if len(observed.Links) != 2 || labels[labelPrefix+"leaf-count"] != "2" || labels[labelPrefix+"leaf-switch"] != nil {
			t.Fatalf("invalid metadata: %#v", observed)
		}
		for _, link := range observed.Links {
			if link.Interface != "bond0" || link.BondName != "bond0" || link.Active {
				t.Fatalf("fabricated Slave attribution: %#v", link)
			}
		}
		return labels, annotations
	}
	labels, annotations := build(neighbors)
	duplicateLabels, duplicateAnnotations := build([]lldpNeighbor{neighbors[1], neighbors[0], neighbors[1]})
	if !reflect.DeepEqual(labels, duplicateLabels) || !reflect.DeepEqual(annotations, duplicateAnnotations) {
		t.Fatal("reordered duplicate frames changed metadata")
	}
}

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
	_, err := receiveLLDP(context.Background(), nil, false, time.Second, 0)
	if err == nil || !strings.Contains(err.Error(), "no selected interface") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCollectionModeMatchesReferenceCollector(t *testing.T) {
	for _, test := range []struct {
		count int
		want  string
	}{
		{count: 2, want: "until-limit"},
		{count: 0, want: "full-window"},
	} {
		if got := collectionMode(test.count); got != test.want {
			t.Fatalf("collectionMode(%d)=%q, want %q", test.count, got, test.want)
		}
	}
}

func TestDistinctLeafCountDeduplicatesSameChassis(t *testing.T) {
	sameLeaf := []lldpNeighbor{
		{LocalInterface: "eno1", ChassisIDSubtype: "mac-address", ChassisID: "00:11:22:33:44:55"},
		{LocalInterface: "eno2", ChassisIDSubtype: "mac-address", ChassisID: "00-11-22-33-44-55"},
	}
	if got := distinctLeafCount(sameLeaf); got != 1 {
		t.Fatalf("same chassis through two links counted as %d Leaves, want 1", got)
	}
	differentLeaves := append([]lldpNeighbor(nil), sameLeaf...)
	differentLeaves[1].ChassisID = "00:11:22:33:44:66"
	if got := distinctLeafCount(differentLeaves); got != 2 {
		t.Fatalf("different chassis counted as %d Leaves, want 2", got)
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
