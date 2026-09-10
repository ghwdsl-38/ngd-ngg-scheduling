package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"
	"time"
)

const ethernetProtocolLLDP = 0x88cc

type lldpNeighbor struct {
	ReceivedAt            time.Time
	LocalInterface        string
	LocalInterfaceIndex   int
	LocalInterfaceKind    string
	LocalInterfaceCarrier string
	LocalInterfaceState   string
	LocalInterfaceLinkUp  bool
	BondMaster            string
	BondMode              string
	BondSlaves            []string
	BondActiveSlave       string
	BondInfoSource        string
	BondSlaveMIIStatus    string
	SourceMAC             string
	DestinationMAC        string
	ChassisIDSubtype      string
	ChassisID             string
	PortIDSubtype         string
	PortID                string
	PortDescription       string
	SystemName            string
	SystemDescription     string
	TTLSeconds            uint16
	ManagementAddresses   []string
	EnabledCapabilities   []string
	SupportedCapabilities []string
	LooksLikeLocalHost    bool
}

func (n lldpNeighbor) switchID() string {
	if n.SystemName != "" {
		return n.SystemName
	}
	return n.ChassisID
}

func parseLLDPFrame(frame []byte, localInterface string) (result lldpNeighbor, valid bool) {
	started := time.Now()
	log.Printf("[LLDP-AGENT] ENTER parseLLDPFrame interface=%s bytes=%d", localInterface, len(frame))
	defer func() {
		log.Printf("[LLDP-AGENT] EXIT parseLLDPFrame interface=%s valid=%t elapsed=%s", localInterface, valid, time.Since(started))
	}()
	if len(frame) < 14 || binary.BigEndian.Uint16(frame[12:14]) != ethernetProtocolLLDP {
		return lldpNeighbor{}, false
	}
	result = lldpNeighbor{
		LocalInterface: localInterface,
		DestinationMAC: net.HardwareAddr(frame[0:6]).String(),
		SourceMAC:      net.HardwareAddr(frame[6:12]).String(),
	}
	payload := frame[14:]
	for offset := 0; offset+2 <= len(payload); {
		header := binary.BigEndian.Uint16(payload[offset : offset+2])
		offset += 2
		kind, length := header>>9, int(header&0x1ff)
		if offset+length > len(payload) {
			log.Printf("[LLDP-AGENT] REJECT malformed TLV type=%d length=%d remaining=%d", kind, length, len(payload)-offset)
			return lldpNeighbor{}, false
		}
		value := payload[offset : offset+length]
		offset += length
		switch kind {
		case 0:
			offset = len(payload)
		case 1:
			result.ChassisIDSubtype, result.ChassisID = decodeIdentifier(value, true)
		case 2:
			result.PortIDSubtype, result.PortID = decodeIdentifier(value, false)
		case 3:
			if len(value) == 2 {
				result.TTLSeconds = binary.BigEndian.Uint16(value)
			}
		case 4:
			result.PortDescription = strings.TrimSpace(string(value))
		case 5:
			result.SystemName = strings.TrimSpace(string(value))
		case 6:
			result.SystemDescription = strings.TrimSpace(string(value))
		case 7:
			result.SupportedCapabilities, result.EnabledCapabilities = decodeCapabilities(value)
		case 8:
			if address := decodeManagementAddress(value); address != "" {
				result.ManagementAddresses = append(result.ManagementAddresses, address)
			}
		}
	}
	return result, result.ChassisID != "" && result.PortID != "" && result.TTLSeconds > 0
}

func decodeIdentifier(value []byte, chassis bool) (string, string) {
	if len(value) < 2 {
		return "", ""
	}
	subtype := int(value[0])
	names := map[int]string{}
	if chassis {
		names = map[int]string{1: "chassis-component", 2: "interface-alias", 3: "port-component", 4: "mac-address", 5: "network-address", 6: "interface-name", 7: "locally-assigned"}
	} else {
		names = map[int]string{1: "interface-alias", 2: "port-component", 3: "mac-address", 4: "network-address", 5: "interface-name", 6: "agent-circuit-id", 7: "locally-assigned"}
	}
	name := names[subtype]
	if name == "" {
		name = fmt.Sprintf("unknown-%d", subtype)
	}
	data := value[1:]
	if name == "mac-address" && len(data) == 6 {
		return name, net.HardwareAddr(data).String()
	}
	if name == "network-address" && len(data) >= 2 {
		return name, decodeNetworkAddress(data)
	}
	return name, strings.TrimSpace(string(data))
}

func decodeNetworkAddress(value []byte) string {
	if len(value) == 5 && value[0] == 1 {
		return net.IP(value[1:]).String()
	}
	if len(value) == 17 && value[0] == 2 {
		return net.IP(value[1:]).String()
	}
	if len(value) < 2 {
		return ""
	}
	return fmt.Sprintf("type-%d:%x", value[0], value[1:])
}

func decodeManagementAddress(value []byte) string {
	if len(value) < 2 {
		return ""
	}
	length := int(value[0])
	if length < 2 || 1+length > len(value) {
		return ""
	}
	return decodeNetworkAddress(value[1 : 1+length])
}

func decodeCapabilities(value []byte) ([]string, []string) {
	if len(value) != 4 {
		return nil, nil
	}
	names := map[uint16]string{
		1 << 0: "other", 1 << 1: "repeater", 1 << 2: "bridge", 1 << 3: "wlan-ap",
		1 << 4: "router", 1 << 5: "telephone", 1 << 6: "docsis", 1 << 7: "station-only",
		1 << 8: "cvlan", 1 << 9: "svlan", 1 << 10: "two-port-mac-relay",
	}
	decode := func(bits uint16) []string {
		result := make([]string, 0)
		for bit, name := range names {
			if bits&bit != 0 {
				result = append(result, name)
			}
		}
		sort.Strings(result)
		return result
	}
	return decode(binary.BigEndian.Uint16(value[:2])), decode(binary.BigEndian.Uint16(value[2:]))
}

func receiveLLDP(ctx context.Context, selections map[string]interfaceSelection, bindSingle bool, timeout time.Duration, count int) ([]lldpNeighbor, error) {
	if len(selections) == 0 {
		return nil, fmt.Errorf("no selected interface; refusing to listen on every host interface")
	}
	return receiveLLDPOnHost(ctx, selections, bindSingle, timeout, count)
}

func applyInterfaceSelection(item *lldpNeighbor, selection interfaceSelection) {
	item.LocalInterfaceIndex = selection.Index
	item.LocalInterfaceKind = selection.Kind
	item.LocalInterfaceCarrier = selection.Carrier
	item.LocalInterfaceState = selection.OperState
	item.LocalInterfaceLinkUp = selection.LinkValid
	item.BondMaster = selection.BondName
	item.BondMode = selection.BondMode
	item.BondSlaves = append([]string(nil), selection.BondSlaves...)
	item.BondActiveSlave = selection.BondActiveSlave
	item.BondInfoSource = selection.BondInfoSource
	item.BondSlaveMIIStatus = selection.MIIStatus
}

func neighborIdentity(item lldpNeighbor) string {
	return strings.Join([]string{item.LocalInterface, item.ChassisIDSubtype, item.ChassisID, item.PortIDSubtype, item.PortID}, "\x00")
}

func sortNeighbors(items []lldpNeighbor) {
	sort.Slice(items, func(i, j int) bool { return neighborIdentity(items[i]) < neighborIdentity(items[j]) })
}

func collectionMode(count int) string {
	if count > 0 {
		return "until-limit"
	}
	return "full-window"
}

// distinctLeafCount deliberately ignores local interface and remote port.
// Two Bond slaves that receive the same chassis are two links to one Leaf,
// not two Leaf switches.
func distinctLeafCount(neighbors []lldpNeighbor) int {
	identities := map[string]struct{}{}
	for _, neighbor := range neighbors {
		if neighbor.LooksLikeLocalHost {
			continue
		}
		if identity := leafChassisIdentity(neighbor); identity != "" {
			identities[identity] = struct{}{}
		}
	}
	return len(identities)
}

func leafChassisIdentity(neighbor lldpNeighbor) string {
	subtype := strings.ToLower(strings.TrimSpace(neighbor.ChassisIDSubtype))
	identifier := strings.ToLower(strings.TrimSpace(neighbor.ChassisID))
	if subtype == "mac-address" {
		identifier = strings.NewReplacer(":", "", "-", "", ".", "").Replace(identifier)
	}
	if identifier == "" {
		return ""
	}
	return subtype + "\x00" + identifier
}

func sameHostName(left, right string) bool {
	normalize := func(value string) string {
		value = strings.ToLower(strings.TrimSpace(value))
		if index := strings.IndexByte(value, '.'); index >= 0 {
			value = value[:index]
		}
		return value
	}
	return normalize(left) != "" && normalize(left) == normalize(right)
}

func sortedInterfaceSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func htons(value uint16) uint16 { return value<<8 | value>>8 }
