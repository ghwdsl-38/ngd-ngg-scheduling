package topologyfacts

import (
	"testing"
	"time"
)

func TestBuildNodeMetadataPersistsOnlyDirectLeafFacts(t *testing.T) {
	observed, labels, annotations, err := BuildNodeMetadata(Observation{
		Source: "LLDP",
		Links:  []Link{{Interface: "eth0", LeafSwitchID: "leaf-a", RemotePortID: "Ethernet1", Active: true}},
	}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.LeafSwitchIDs) != 1 || observed.LeafSwitchIDs[0] != "leaf-a" {
		t.Fatalf("unexpected Leaf set: %#v", observed.LeafSwitchIDs)
	}
	if labels[Prefix+"leaf-switch"] != "leaf-a" || labels[Prefix+"leaf-count"] != "1" {
		t.Fatalf("unexpected Node labels: %#v", labels)
	}
	if annotations[Prefix+"source"] != "LLDP" {
		t.Fatalf("unexpected Node annotations: %#v", annotations)
	}
	for _, forbidden := range []string{"border-switch", "spine-switch", "data-center", "region"} {
		if _, exists := labels[Prefix+forbidden]; exists {
			t.Fatalf("upper topology %s must remain in Algorithm configuration", forbidden)
		}
	}
}

func TestNormalizeCreatesStableDualLeafIdentity(t *testing.T) {
	first, err := Normalize(Observation{Links: []Link{
		{Interface: "eth1", LeafSwitchID: "leaf-b", Active: true},
		{Interface: "eth0", LeafSwitchID: "leaf-a", Active: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Normalize(Observation{Links: []Link{
		{Interface: "eth0", LeafSwitchID: "leaf-a", Active: true},
		{Interface: "eth1", LeafSwitchID: "leaf-b", Active: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if LeafSetID(first.LeafSwitchIDs) != LeafSetID(second.LeafSwitchIDs) {
		t.Fatalf("input order changed Leaf set identity: first=%v second=%v", first.LeafSwitchIDs, second.LeafSwitchIDs)
	}
}

func TestNormalizeRetainsDistinctPeersOnSameInterface(t *testing.T) {
	observed, err := Normalize(Observation{Links: []Link{
		{Interface: "eth0", LeafSwitchID: "leaf-a", RemotePortID: "Ethernet1", Active: true},
		{Interface: "eth0", LeafSwitchID: "leaf-b", RemotePortID: "Ethernet2", Active: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.Links) != 2 || len(observed.LeafSwitchIDs) != 2 {
		t.Fatalf("distinct LLDP peers were collapsed: %#v", observed)
	}
}

func TestNormalizeDeduplicatesLeafSetAcrossTwoPhysicalLinks(t *testing.T) {
	observed, err := Normalize(Observation{Links: []Link{
		{Interface: "eth0", LeafSwitchID: "leaf-a", RemotePortID: "Ethernet1", Active: true},
		{Interface: "eth1", LeafSwitchID: "leaf-a", RemotePortID: "Ethernet2", Active: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.Links) != 2 || len(observed.LeafSwitchIDs) != 1 || observed.LeafSwitchIDs[0] != "leaf-a" {
		t.Fatalf("two links to one Leaf were not represented correctly: %#v", observed)
	}
	if LeafSetID([]string{"leaf-a", "leaf-a"}) != LeafSetID([]string{"leaf-a"}) {
		t.Fatal("duplicate Leaf values changed the stable Leaf set ID")
	}
}

func TestNormalizeSupportsMoreThanTwoExplicitLeaves(t *testing.T) {
	observed, err := Normalize(Observation{Links: []Link{
		{Interface: "eth0", LeafSwitchID: "leaf-a", RemotePortID: "1"},
		{Interface: "eth0", LeafSwitchID: "leaf-b", RemotePortID: "2"},
		{Interface: "eth1", LeafSwitchID: "leaf-c", RemotePortID: "3"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.LeafSwitchIDs) != 3 {
		t.Fatalf("expected all three explicit Leaves, got %#v", observed.LeafSwitchIDs)
	}
}
