// Package bond discovers physical wired interfaces and Linux Bond devices for
// LLDP collection. Selection is kept separate from packet capture so it can be
// tested with a synthetic sysfs tree on machines without real LLDP neighbors.
package bond

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// InterfaceSelection describes one physical interface selected for LLDP.
type InterfaceSelection struct {
	Name             string
	Index            int
	Kind             string
	BondName         string
	BondMode         string
	BondSlaves       []string
	BondActiveSlave  string
	BondInfoSource   string
	Active           bool
	AdministrativeUp bool
	LinkValid        bool
	PhysicalWired    bool
	Carrier          string
	OperState        string
	MIIStatus        string
}

type bondState struct {
	Name        string
	Mode        string
	Slaves      []string
	ActiveSlave string
	SlaveMII    map[string]string
	Source      string
}

const preferredBondName = "bond0"

// SelectInterfaces reads the real Linux sysfs and /proc Bond state.
func SelectInterfaces(sysClassNet string, explicit map[string]struct{}) (map[string]InterfaceSelection, error) {
	log.Printf("[LLDP-AGENT] STEP 1/5 START get all interfaces")
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("enumerate interfaces: %w", err)
	}
	inventory := make(map[string]net.Interface, len(interfaces))
	for _, iface := range interfaces {
		inventory[iface.Name] = iface
		log.Printf("[LLDP-AGENT] STEP 1/5 INTERFACE name=%s index=%d mac=%s flags=%s", iface.Name, iface.Index, iface.HardwareAddr, iface.Flags)
	}
	log.Printf("[LLDP-AGENT] STEP 1/5 COMPLETE totalInterfaces=%d", len(interfaces))
	return selectInterfacesAt(sysClassNet, "/proc/net/bonding", explicit, inventory, true)
}

// SelectInterfacesAt is the testable implementation. Explicit interface names
// are trusted as an operator override. Automatic mode excludes virtual,
// wireless and down links, expands Bond masters to eligible physical slaves,
// and never falls back to listening on every interface.
func SelectInterfacesAt(sysClassNet, procBonding string, explicit map[string]struct{}) (map[string]InterfaceSelection, error) {
	return selectInterfacesAt(sysClassNet, procBonding, explicit, nil, false)
}

func selectInterfacesAt(sysClassNet, procBonding string, explicit map[string]struct{}, inventory map[string]net.Interface, enforceRuntime bool) (map[string]InterfaceSelection, error) {
	mode := "automatic"
	if len(explicit) > 0 {
		mode = "explicit"
	}
	log.Printf("[LLDP-AGENT] INTERFACE DISCOVERY START mode=%s configured=%v sysClassNet=%q", mode, sortedSet(explicit), sysClassNet)
	entries, err := os.ReadDir(sysClassNet)
	if err != nil {
		return nil, fmt.Errorf("read network sysfs %s: %w", sysClassNet, err)
	}

	bonds := discoverBonds(sysClassNet, procBonding, entries)
	for _, state := range sortedBondStates(bonds) {
		log.Printf("[LLDP-AGENT] BOND DISCOVERED name=%s mode=%s slaves=%v activeSlave=%q source=%s", state.Name, state.Mode, state.Slaves, state.ActiveSlave, state.Source)
	}
	selected := map[string]InterfaceSelection{}
	knownSlaves := map[string]struct{}{}
	_, preferredBondExists := bonds[preferredBondName]
	for _, state := range sortedBondStates(bonds) {
		for _, slave := range state.Slaves {
			knownSlaves[slave] = struct{}{}
			if len(explicit) != 0 {
				continue
			}
			// Production workers use bond0 as their service uplink. When it is
			// present, do not mix management/storage links or another Bond into
			// the scheduling topology. A host without bond0 keeps the verified
			// lldp-new-2 automatic-discovery fallback below.
			if preferredBondExists && state.Name != preferredBondName {
				continue
			}
			carrier, operState := linkState(sysClassNet, slave)
			index, administrativeUp, linkValid := runtimeLinkState(slave, carrier, operState, inventory, enforceRuntime)
			miiStatus := strings.ToLower(strings.TrimSpace(state.SlaveMII[slave]))
			active := slave == state.ActiveSlave
			physicalWired := isPhysicalWired(sysClassNet, slave)
			if !physicalWired {
				continue
			}
			if state.Mode == "active-backup" && !active {
				continue
			}
			if miiStatus != "" && miiStatus != "up" {
				continue
			}
			if !linkValid {
				continue
			}
			if _, err := os.Stat(filepath.Join(sysClassNet, slave)); err != nil {
				continue
			}
			selected[slave] = InterfaceSelection{
				Name: slave, Index: index, Kind: "bond-slave", BondName: state.Name, BondMode: state.Mode,
				BondSlaves: append([]string(nil), state.Slaves...), BondActiveSlave: state.ActiveSlave,
				BondInfoSource: state.Source, Active: active || state.Mode != "active-backup",
				AdministrativeUp: administrativeUp, LinkValid: linkValid, PhysicalWired: physicalWired,
				Carrier: carrier, OperState: operState, MIIStatus: miiStatus,
			}
		}
	}

	// An explicit Bond master limits collection to that Bond, but expands it to
	// its eligible physical slaves. Unlike automatic active-backup selection,
	// explicit mode keeps every link-valid slave so an operator can inventory all
	// Leaf neighbors in the requested interface scope. Exact physical/slave names
	// remain exact operator overrides.
	for name := range explicit {
		if state, isBond := bonds[name]; isBond {
			log.Printf("[LLDP-AGENT] EXPLICIT BOND EXPAND START master=%s mode=%s declaredSlaves=%v activeSlave=%q", state.Name, state.Mode, state.Slaves, state.ActiveSlave)
			expanded := make([]string, 0, len(state.Slaves))
			for _, slave := range state.Slaves {
				selection, reason, eligible := bondSlaveSelection(sysClassNet, state, slave, inventory, enforceRuntime, true)
				if !eligible {
					log.Printf("[LLDP-AGENT] EXPLICIT BOND SLAVE REJECT master=%s slave=%s reason=%s", state.Name, slave, reason)
					continue
				}
				selected[slave] = selection
				expanded = append(expanded, slave)
			}
			sort.Strings(expanded)
			log.Printf("[LLDP-AGENT] EXPLICIT BOND EXPAND COMPLETE master=%s selectedSlaves=%v", state.Name, expanded)
			continue
		}
		if _, err := os.Stat(filepath.Join(sysClassNet, name)); err == nil {
			carrier, operState := linkState(sysClassNet, name)
			index, administrativeUp, linkValid := runtimeLinkState(name, carrier, operState, inventory, enforceRuntime)
			if !administrativeUp {
				continue
			}
			kind := classifyInterface(sysClassNet, name)
			bondName, bondMode, bondSlaves, bondActiveSlave, bondInfoSource, miiStatus := "", "direct", []string(nil), "", "", ""
			if state, isSlave := bondForSlave(bonds, name); isSlave {
				kind, bondName, bondMode = "bond-slave", state.Name, state.Mode
				bondSlaves, bondActiveSlave, bondInfoSource = append([]string(nil), state.Slaves...), state.ActiveSlave, state.Source
				miiStatus = strings.ToLower(strings.TrimSpace(state.SlaveMII[name]))
			}
			selected[name] = InterfaceSelection{
				Name: name, Index: index, Kind: kind, BondName: bondName, BondMode: bondMode,
				BondSlaves: bondSlaves, BondActiveSlave: bondActiveSlave, BondInfoSource: bondInfoSource,
				Active:           bondMode != "active-backup" || name == bondActiveSlave,
				AdministrativeUp: administrativeUp, LinkValid: linkValid,
				PhysicalWired: isPhysicalWired(sysClassNet, name), Carrier: carrier, OperState: operState, MIIStatus: miiStatus,
			}
		} else {
			log.Printf("[LLDP-AGENT] EXPLICIT INTERFACE REJECT name=%s reason=not-found-in-sysfs", name)
		}
	}

	// Automatic mode adds only standalone physical Ethernet interfaces. It
	// deliberately excludes CNI/veth/poh/bridge devices to avoid self-reflected
	// LLDP frames being interpreted as upstream Leaf switches.
	if len(explicit) == 0 && !preferredBondExists {
		for _, entry := range entries {
			name := entry.Name()
			if _, isSlave := knownSlaves[name]; isSlave {
				continue
			}
			if _, isBond := bonds[name]; isBond {
				continue
			}
			if !isPhysicalWired(sysClassNet, name) {
				continue
			}
			carrier, operState := linkState(sysClassNet, name)
			index, administrativeUp, linkValid := runtimeLinkState(name, carrier, operState, inventory, enforceRuntime)
			if !linkValid {
				continue
			}
			selected[name] = InterfaceSelection{
				Name: name, Index: index, Kind: "physical", BondMode: "direct", Active: true,
				AdministrativeUp: administrativeUp, LinkValid: linkValid, PhysicalWired: true,
				Carrier: carrier, OperState: operState,
			}
		}
	}

	if len(selected) == 0 {
		if len(explicit) > 0 {
			return nil, fmt.Errorf("no eligible LLDP interface found for configured interfaces %v", sortedSet(explicit))
		}
		if preferredBondExists {
			return nil, fmt.Errorf("preferred Bond %s exists but has no eligible LLDP slave; refusing to fall back to unrelated host interfaces", preferredBondName)
		}
		return nil, fmt.Errorf("automatic discovery found no eligible physical wired interface or Bond slave")
	}
	if preferredBondExists && len(explicit) == 0 {
		log.Printf("[LLDP-AGENT] BOND PRIORITY selected=%s policy=do-not-mix-unrelated-interfaces", preferredBondName)
	}
	for _, item := range Sorted(selected) {
		log.Printf("[LLDP-AGENT] FINAL USABLE INTERFACE name=%s index=%d kind=%s adminUp=%t carrier=%q operState=%q physicalWired=%t bondMaster=%q bondMode=%q bondInfoSource=%q bondMIIStatus=%q", item.Name, item.Index, item.Kind, item.AdministrativeUp, item.Carrier, item.OperState, item.PhysicalWired, item.BondName, item.BondMode, item.BondInfoSource, item.MIIStatus)
	}
	log.Printf("[LLDP-AGENT] INTERFACE DISCOVERY COMPLETE mode=%s selected=%v", mode, selectedNames(selected))
	return selected, nil
}

func bondSlaveSelection(sysClassNet string, state bondState, slave string, inventory map[string]net.Interface, enforceRuntime, includeStandby bool) (InterfaceSelection, string, bool) {
	carrier, operState := linkState(sysClassNet, slave)
	index, administrativeUp, linkValid := runtimeLinkState(slave, carrier, operState, inventory, enforceRuntime)
	miiStatus := strings.ToLower(strings.TrimSpace(state.SlaveMII[slave]))
	active := slave == state.ActiveSlave
	physicalWired := isPhysicalWired(sysClassNet, slave)
	switch {
	case !physicalWired:
		return InterfaceSelection{}, "not-physical-wired", false
	case state.Mode == "active-backup" && !includeStandby && !active:
		return InterfaceSelection{}, "standby-slave-in-automatic-active-backup", false
	case miiStatus != "" && miiStatus != "up":
		return InterfaceSelection{}, "bond-mii-not-up", false
	case !linkValid:
		return InterfaceSelection{}, "link-not-up", false
	}
	if _, err := os.Stat(filepath.Join(sysClassNet, slave)); err != nil {
		return InterfaceSelection{}, "missing-from-sysfs", false
	}
	return InterfaceSelection{
		Name: slave, Index: index, Kind: "bond-slave", BondName: state.Name, BondMode: state.Mode,
		BondSlaves: append([]string(nil), state.Slaves...), BondActiveSlave: state.ActiveSlave,
		BondInfoSource: state.Source, Active: active || state.Mode != "active-backup",
		AdministrativeUp: administrativeUp, LinkValid: linkValid, PhysicalWired: physicalWired,
		Carrier: carrier, OperState: operState, MIIStatus: miiStatus,
	}, "", true
}

func selectedNames(values map[string]InterfaceSelection) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func bondForSlave(values map[string]bondState, slave string) (bondState, bool) {
	for _, state := range values {
		for _, candidate := range state.Slaves {
			if candidate == slave {
				return state, true
			}
		}
	}
	return bondState{}, false
}

func runtimeLinkState(name, carrier, operState string, inventory map[string]net.Interface, enforceRuntime bool) (int, bool, bool) {
	if !enforceRuntime {
		up := linkUpValues(carrier, operState)
		return 0, up, up
	}
	iface, exists := inventory[name]
	if !exists {
		return 0, false, false
	}
	administrativeUp := iface.Flags&net.FlagUp != 0
	return iface.Index, administrativeUp, administrativeUp && linkUpValues(carrier, operState)
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

func discoverBonds(sysClassNet, procBonding string, entries []os.DirEntry) map[string]bondState {
	result := map[string]bondState{}
	for _, entry := range entries {
		name := entry.Name()
		bondingPath := filepath.Join(sysClassNet, name, "bonding")
		modeRaw, err := os.ReadFile(filepath.Join(bondingPath, "mode"))
		if err != nil {
			continue
		}
		slavesRaw, _ := os.ReadFile(filepath.Join(bondingPath, "slaves"))
		activeRaw, _ := os.ReadFile(filepath.Join(bondingPath, "active_slave"))
		result[name] = bondState{
			Name: name, Mode: normalizeMode(string(modeRaw)), Slaves: strings.Fields(string(slavesRaw)),
			ActiveSlave: strings.TrimSpace(string(activeRaw)), SlaveMII: map[string]string{}, Source: "sysfs",
		}
	}

	procEntries, err := os.ReadDir(procBonding)
	if err != nil {
		return result
	}
	for _, entry := range procEntries {
		if entry.IsDir() {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(procBonding, entry.Name()))
		if err != nil {
			continue
		}
		parsed := parseProcBond(entry.Name(), string(payload))
		current, exists := result[entry.Name()]
		if !exists {
			parsed.Source = "/proc/net/bonding"
			result[entry.Name()] = parsed
			continue
		}
		if current.Mode == "" || current.Mode == "unknown" {
			current.Mode = parsed.Mode
		}
		if len(current.Slaves) == 0 {
			current.Slaves = parsed.Slaves
		}
		if current.ActiveSlave == "" || current.ActiveSlave == "None" {
			current.ActiveSlave = parsed.ActiveSlave
		}
		current.SlaveMII = parsed.SlaveMII
		current.Source = "sysfs+/proc/net/bonding"
		result[entry.Name()] = current
	}
	return result
}

func parseProcBond(name, payload string) bondState {
	result := bondState{Name: name, SlaveMII: map[string]string{}}
	currentSlave := ""
	for _, raw := range strings.Split(payload, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(raw), ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "Bonding Mode":
			result.Mode = normalizeProcMode(value)
		case "Currently Active Slave":
			if value != "None" {
				result.ActiveSlave = value
			}
		case "Slave Interface":
			currentSlave = value
			result.Slaves = append(result.Slaves, value)
		case "MII Status":
			if currentSlave != "" {
				result.SlaveMII[currentSlave] = strings.ToLower(value)
			}
		}
	}
	return result
}

func sortedBondStates(values map[string]bondState) []bondState {
	result := make([]bondState, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func isPhysicalWired(sysClassNet, name string) bool {
	path := filepath.Join(sysClassNet, name)
	if _, err := os.Stat(filepath.Join(path, "device")); err != nil {
		return false
	}
	if strings.TrimSpace(readFile(filepath.Join(path, "type"))) != "1" {
		return false
	}
	if _, err := os.Stat(filepath.Join(path, "wireless")); err == nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(path, "phy80211")); err == nil {
		return false
	}
	return true
}

func classifyInterface(sysClassNet, name string) string {
	if isPhysicalWired(sysClassNet, name) {
		return "physical"
	}
	return "explicit-virtual-or-unknown"
}

func linkState(sysClassNet, name string) (string, string) {
	return strings.TrimSpace(readFile(filepath.Join(sysClassNet, name, "carrier"))),
		strings.TrimSpace(readFile(filepath.Join(sysClassNet, name, "operstate")))
}

func linkUpValues(carrier, operState string) bool {
	return carrier == "1" || strings.EqualFold(operState, "up")
}

func readFile(path string) string {
	value, _ := os.ReadFile(path)
	return string(value)
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

func normalizeProcMode(value string) string {
	lower := strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(lower, "active-backup"):
		return "active-backup"
	case strings.Contains(lower, "802.3ad"):
		return "802.3ad"
	case strings.Contains(lower, "balance-xor"):
		return "balance-xor"
	case strings.Contains(lower, "round-robin"):
		return "balance-rr"
	case strings.Contains(lower, "broadcast"):
		return "broadcast"
	case strings.Contains(lower, "transmit load balancing"):
		return "balance-tlb"
	case strings.Contains(lower, "adaptive load balancing"):
		return "balance-alb"
	default:
		return lower
	}
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
