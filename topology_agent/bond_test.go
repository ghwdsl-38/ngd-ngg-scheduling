package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestBondMasterCapture(t *testing.T) {
	for _, mode := range []string{"active-backup 1", "802.3ad 4"} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/explicit=%t", mode, explicit), func(t *testing.T) {
				root := t.TempDir()
				writeBondFixture(t, root, "bond0", mode, "eth0 eth1", "eth0")
				writePhysicalFixture(t, root, "eth0", "1", "up")
				writePhysicalFixture(t, root, "eth1", "1", "up")
				var scope map[string]struct{}
				if explicit {
					scope = map[string]struct{}{"bond0": {}}
				}
				selected, err := selectLLDPInterfacesAt(root, t.TempDir(), scope)
				if err != nil {
					t.Fatal(err)
				}
				master, ok := selected["bond0"]
				if !ok || len(selected) != 1 || master.Kind != "bond-master" || master.Active || !master.LinkValid || len(master.BondSlaves) != 2 || master.BondActiveSlave != "eth0" {
					t.Fatalf("unexpected Bond capture: %#v", selected)
				}
				if shouldBindSingleExplicitInterface(scope, selected) != explicit {
					t.Fatal("incorrect bind policy")
				}
			})
		}
	}
}

func TestDownBondMasterIsRejected(t *testing.T) {
	root := t.TempDir()
	writeBondFixture(t, root, "bond0", "802.3ad 4", "eth0 eth1", "eth0")
	writeLinkFixture(t, root, "bond0", "0", "down")
	if _, err := selectLLDPInterfacesAt(root, t.TempDir(), map[string]struct{}{"bond0": {}}); err == nil {
		t.Fatal("down Bond accepted")
	}
}

func TestExplicitBondSlaveRemainsExactInterface(t *testing.T) {
	root := t.TempDir()
	writeBondFixture(t, root, "bond0", "802.3ad 4", "eth0 eth1", "eth0")
	writePhysicalFixture(t, root, "eth0", "1", "up")
	writePhysicalFixture(t, root, "eth1", "1", "up")

	selected, err := selectLLDPInterfacesAt(root, t.TempDir(), map[string]struct{}{"eth1": {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected["eth1"].BondName != "bond0" {
		t.Fatalf("explicit Bond slave must remain an exact selection: %#v", selected)
	}
}

func TestAutomaticDiscoveryKeepsBondAndStandaloneUplinks(t *testing.T) {
	root := t.TempDir()
	writeBondFixture(t, root, "bond0", "802.3ad 4", "eth0 eth1", "eth0")
	writePhysicalFixture(t, root, "eth0", "1", "up")
	writePhysicalFixture(t, root, "eth1", "1", "up")
	writePhysicalFixture(t, root, "management0", "1", "up")

	selected, err := selectLLDPInterfacesAt(root, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected["bond0"].Kind != "bond-master" {
		t.Fatalf("automatic reference policy did not select all eligible uplinks: %#v", selected)
	}
	if _, exists := selected["management0"]; !exists {
		t.Fatalf("standalone physical uplink should remain in auto scope: %#v", selected)
	}
}

func TestAutomaticDiscoveryFallsBackWhenBond0IsUnusable(t *testing.T) {
	root := t.TempDir()
	writeBondFixture(t, root, "bond0", "802.3ad 4", "eth0 eth1", "eth0")
	writeLinkFixture(t, root, "bond0", "0", "down")
	writePhysicalFixture(t, root, "eth0", "0", "down")
	writePhysicalFixture(t, root, "eth1", "0", "down")
	writePhysicalFixture(t, root, "management0", "1", "up")

	selected, err := selectLLDPInterfacesAt(root, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected["management0"].Kind != "physical" {
		t.Fatalf("automatic fallback did not retain the healthy physical uplink: %#v", selected)
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

func TestProcBondingFallbackSelectsBondMaster(t *testing.T) {
	root := t.TempDir()
	proc := t.TempDir()
	writeLinkFixture(t, root, "bond0", "1", "up")
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
	if len(selected) != 1 || selected["bond0"].BondName != "bond0" || selected["bond0"].BondActiveSlave != "ens2" || selected["bond0"].Active {
		t.Fatalf("unexpected /proc active-backup selection: %#v", selected)
	}
}

func writeBondFixture(t *testing.T, root, bond, mode, slaves, active string) {
	t.Helper()
	writeLinkFixture(t, root, bond, "1", "up")
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
