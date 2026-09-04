package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestActiveBackupSelectsOnlyActiveSlave(t *testing.T) {
	root := t.TempDir()
	writeBondFixture(t, root, "bond0", "active-backup 1", "eth0 eth1", "eth1")
	writeLinkFixture(t, root, "eth0", "1", "up")
	writeLinkFixture(t, root, "eth1", "1", "up")
	selected, err := selectLLDPInterfaces(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected["eth1"].BondMode != "active-backup" || !selected["eth1"].Active {
		t.Fatalf("unexpected active-backup selection: %#v", selected)
	}
}

func TestLACPSelectsAllUpSlaves(t *testing.T) {
	root := t.TempDir()
	writeBondFixture(t, root, "bond0", "802.3ad 4", "eth0 eth1 eth2", "eth0")
	writeLinkFixture(t, root, "eth0", "1", "up")
	writeLinkFixture(t, root, "eth1", "1", "up")
	writeLinkFixture(t, root, "eth2", "0", "down")
	selected, err := selectLLDPInterfaces(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected["eth0"].BondMode != "802.3ad" || selected["eth1"].Name != "eth1" {
		t.Fatalf("unexpected LACP selection: %#v", selected)
	}
	if _, exists := selected["eth2"]; exists {
		t.Fatalf("down slave was selected: %#v", selected)
	}
}

func writeBondFixture(t *testing.T, root, bond, mode, slaves, active string) {
	t.Helper()
	directory := filepath.Join(root, bond, "bonding")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"mode": mode, "slaves": slaves, "active_slave": active} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeLinkFixture(t *testing.T, root, name, carrier, state string) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "carrier"), []byte(carrier), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "operstate"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
}
