package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActiveBackupSelectsOnlyActiveSlave(t *testing.T) {
	root := t.TempDir()
	writeBondFixture(t, root, "bond0", "active-backup 1", "eth0 eth1", "eth1")
	writePhysicalFixture(t, root, "eth0", "1", "up")
	writePhysicalFixture(t, root, "eth1", "1", "up")
	selected, err := selectLLDPInterfacesAt(root, t.TempDir(), nil)
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
	writePhysicalFixture(t, root, "eth0", "1", "up")
	writePhysicalFixture(t, root, "eth1", "1", "up")
	writePhysicalFixture(t, root, "eth2", "0", "down")
	selected, err := selectLLDPInterfacesAt(root, t.TempDir(), nil)
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

func TestBond0TakesPriorityOverOtherPhysicalUplinks(t *testing.T) {
	root := t.TempDir()
	writeBondFixture(t, root, "bond0", "802.3ad 4", "eth0 eth1", "eth0")
	writePhysicalFixture(t, root, "eth0", "1", "up")
	writePhysicalFixture(t, root, "eth1", "1", "up")
	writePhysicalFixture(t, root, "management0", "1", "up")

	selected, err := selectLLDPInterfacesAt(root, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected["eth0"].BondName != "bond0" || selected["eth1"].BondName != "bond0" {
		t.Fatalf("bond0 was not selected exclusively: %#v", selected)
	}
	if _, exists := selected["management0"]; exists {
		t.Fatalf("standalone management link was mixed into bond0 topology: %#v", selected)
	}
}

func TestExistingButUnusableBond0FailsClosed(t *testing.T) {
	root := t.TempDir()
	writeBondFixture(t, root, "bond0", "802.3ad 4", "eth0 eth1", "eth0")
	writePhysicalFixture(t, root, "eth0", "0", "down")
	writePhysicalFixture(t, root, "eth1", "0", "down")
	writePhysicalFixture(t, root, "management0", "1", "up")

	selected, err := selectLLDPInterfacesAt(root, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "preferred Bond bond0 exists") {
		t.Fatalf("expected fail-closed bond0 error, selected=%#v err=%v", selected, err)
	}
}

func TestAutomaticDiscoverySelectsPhysicalAndRejectsVirtual(t *testing.T) {
	root := t.TempDir()
	proc := t.TempDir()
	writePhysicalFixture(t, root, "ens5f1np1", "1", "up")
	writeLinkFixture(t, root, "poh_C04xs4Q", "1", "up")

	selected, err := selectLLDPInterfacesAt(root, proc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected["ens5f1np1"].Kind != "physical" {
		t.Fatalf("automatic discovery did not select only the physical uplink: %#v", selected)
	}
	if _, exists := selected["poh_C04xs4Q"]; exists {
		t.Fatalf("virtual poh interface must not be selected: %#v", selected)
	}
}

func TestAutomaticDiscoveryRejectsVirtualOnlyEnvironment(t *testing.T) {
	root := t.TempDir()
	writeLinkFixture(t, root, "lo", "1", "unknown")
	writeLinkFixture(t, root, "veth-test", "1", "up")
	writeLinkFixture(t, root, "poh_C04xs4Q", "1", "up")

	selected, err := selectLLDPInterfacesAt(root, t.TempDir(), nil)
	if err == nil {
		t.Fatalf("expected virtual-only environment to fail closed, selected=%#v", selected)
	}
}

func TestExplicitVirtualInterfaceRemainsOperatorOverride(t *testing.T) {
	root := t.TempDir()
	writeLinkFixture(t, root, "veth-test", "1", "up")
	selected, err := selectLLDPInterfacesAt(root, t.TempDir(), map[string]struct{}{"veth-test": {}})
	if err != nil {
		t.Fatal(err)
	}
	if selected["veth-test"].Kind != "explicit-virtual-or-unknown" {
		t.Fatalf("unexpected explicit interface result: %#v", selected)
	}
}

func TestProcBondingFallbackSelectsActiveBackupSlave(t *testing.T) {
	root := t.TempDir()
	proc := t.TempDir()
	writePhysicalFixture(t, root, "ens1", "1", "up")
	writePhysicalFixture(t, root, "ens2", "1", "up")
	content := `Ethernet Channel Bonding Driver
Bonding Mode: fault-tolerance (active-backup)
Currently Active Slave: ens2
MII Status: up
Slave Interface: ens1
MII Status: up
Slave Interface: ens2
MII Status: up
`
	if err := os.WriteFile(filepath.Join(proc, "bond0"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	selected, err := selectLLDPInterfacesAt(root, proc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected["ens2"].BondName != "bond0" || !selected["ens2"].Active {
		t.Fatalf("unexpected /proc active-backup selection: %#v", selected)
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

func writePhysicalFixture(t *testing.T, root, name, carrier, state string) {
	t.Helper()
	writeLinkFixture(t, root, name, carrier, state)
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(directory, "device"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "type"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
}
