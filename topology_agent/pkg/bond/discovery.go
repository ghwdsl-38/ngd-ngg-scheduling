// Package bond discovers Linux Bond devices and selects interfaces eligible
// for LLDP collection. It is shared by the production Agent and integration
// tests so Bond semantics cannot drift between them.
package bond

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// InterfaceSelection describes one physical interface selected for LLDP.
type InterfaceSelection struct {
	Name     string
	BondName string
	BondMode string
	Active   bool
}

// SelectInterfaces inspects Linux bonding sysfs. Explicit interfaces are a
// filter; naming a Bond master selects its eligible slaves.
func SelectInterfaces(sysClassNet string, explicit map[string]struct{}) (map[string]InterfaceSelection, error) {
	entries, err := os.ReadDir(sysClassNet)
	if err != nil {
		return nil, fmt.Errorf("read network sysfs %s: %w", sysClassNet, err)
	}
	selected := map[string]InterfaceSelection{}
	knownSlaves := map[string]struct{}{}
	foundBond := false
	for _, entry := range entries {
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		bondName := entry.Name()
		bondingPath := filepath.Join(sysClassNet, bondName, "bonding")
		modeRaw, readErr := os.ReadFile(filepath.Join(bondingPath, "mode"))
		if readErr != nil {
			continue
		}
		foundBond = true
		mode := normalizeMode(string(modeRaw))
		slavesRaw, readErr := os.ReadFile(filepath.Join(bondingPath, "slaves"))
		if readErr != nil {
			return nil, fmt.Errorf("read slaves for Bond %s: %w", bondName, readErr)
		}
		slaves := strings.Fields(string(slavesRaw))
		activeRaw, _ := os.ReadFile(filepath.Join(bondingPath, "active_slave"))
		activeSlave := strings.TrimSpace(string(activeRaw))
		for _, slave := range slaves {
			knownSlaves[slave] = struct{}{}
			if !interfaceRequested(explicit, bondName, slave) {
				continue
			}
			active := slave == activeSlave
			if mode == "active-backup" && !active {
				continue
			}
			if mode != "active-backup" && !linkUp(sysClassNet, slave) {
				continue
			}
			selected[slave] = InterfaceSelection{Name: slave, BondName: bondName, BondMode: mode, Active: active || mode != "active-backup"}
		}
	}

	for name := range explicit {
		if _, isSlave := knownSlaves[name]; isSlave {
			continue
		}
		if _, exists := selected[name]; exists {
			continue
		}
		if _, err := os.Stat(filepath.Join(sysClassNet, name)); err == nil {
			selected[name] = InterfaceSelection{Name: name, BondMode: "direct", Active: true}
		}
	}

	if len(selected) == 0 && foundBond {
		return nil, fmt.Errorf("no eligible LLDP interface found on configured Bond devices")
	}
	return selected, nil
}

// Sorted returns deterministic interface order for evidence and collection.
func Sorted(values map[string]InterfaceSelection) []InterfaceSelection {
	result := make([]InterfaceSelection, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func interfaceRequested(explicit map[string]struct{}, bondName, slave string) bool {
	if len(explicit) == 0 {
		return true
	}
	_, bondSelected := explicit[bondName]
	_, slaveSelected := explicit[slave]
	return bondSelected || slaveSelected
}

func linkUp(sysClassNet, name string) bool {
	carrier, carrierErr := os.ReadFile(filepath.Join(sysClassNet, name, "carrier"))
	operstate, operErr := os.ReadFile(filepath.Join(sysClassNet, name, "operstate"))
	if carrierErr == nil && strings.TrimSpace(string(carrier)) != "1" {
		return false
	}
	if operErr == nil && strings.EqualFold(strings.TrimSpace(string(operstate)), "down") {
		return false
	}
	return carrierErr == nil || operErr == nil
}

func normalizeMode(raw string) string {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return "unknown"
	}
	mode := strings.ToLower(fields[0])
	aliases := map[string]string{
		"1": "active-backup", "0": "balance-rr", "2": "balance-xor", "3": "broadcast",
		"4": "802.3ad", "5": "balance-tlb", "6": "balance-alb",
	}
	if normalized, ok := aliases[mode]; ok {
		return normalized
	}
	return mode
}
