package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
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

func receiveLLDP(ctx context.Context, selections map[string]interfaceSelection, automatic bool, timeout, idleTimeout time.Duration, count int) (result []lldpNeighbor, returnErr error) {
	started := time.Now()
	allowed := make(map[string]struct{}, len(selections))
	candidates := make(map[int]interfaceSelection, len(selections))
	for name, selection := range selections {
		allowed[name] = struct{}{}
		iface, err := net.InterfaceByName(name)
		if err != nil {
			return nil, fmt.Errorf("resolve selected interface %q: %w", name, err)
		}
		selection.Index = iface.Index
		candidates[iface.Index] = selection
	}
	allowedNames := sortedInterfaceSet(allowed)
	log.Printf("[LLDP-AGENT] ENTER receiveLLDP automatic=%t interfaces=%v timeout=%s idleTimeout=%s count=%d", automatic, allowedNames, timeout, idleTimeout, count)
	defer func() {
		log.Printf("[LLDP-AGENT] EXIT receiveLLDP neighbors=%d elapsed=%s error=%v", len(result), time.Since(started), returnErr)
	}()
	if len(selections) == 0 {
		return nil, fmt.Errorf("no selected interface; refusing to listen on every host interface")
	}

	log.Printf("[LLDP-AGENT] CALL unix.Socket family=AF_PACKET type=SOCK_RAW protocol=0x%04x", ethernetProtocolLLDP)
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(htons(ethernetProtocolLLDP)))
	if err != nil {
		return nil, fmt.Errorf("open LLDP raw socket: %w; run as root or grant CAP_NET_RAW", err)
	}
	defer unix.Close(fd)
	log.Printf("[LLDP-AGENT] RETURN unix.Socket fd=%d", fd)

	// Keep the tested lldp-new-2 behavior: automatic discovery always opens one
	// protocol-scoped socket and filters selected ifindexes in userspace. Only a
	// single explicitly configured interface is bound in the kernel.
	if !automatic && len(selections) == 1 {
		iface, err := net.InterfaceByName(allowedNames[0])
		if err != nil {
			return nil, fmt.Errorf("resolve selected interface %q: %w", allowedNames[0], err)
		}
		address := &unix.SockaddrLinklayer{Protocol: htons(ethernetProtocolLLDP), Ifindex: iface.Index}
		log.Printf("[LLDP-AGENT] CALL unix.Bind interface=%s ifindex=%d", iface.Name, iface.Index)
		if err := unix.Bind(fd, address); err != nil {
			return nil, fmt.Errorf("bind LLDP socket to %q: %w", iface.Name, err)
		}
		log.Printf("[LLDP-AGENT] RETURN unix.Bind status=success")
	} else if automatic {
		log.Printf("[LLDP-AGENT] SKIP unix.Bind reason=auto-discovery kernelScope=all userspaceFilter=selected-ifindexes")
	} else {
		log.Printf("[LLDP-AGENT] SKIP unix.Bind reason=multiple-explicit-interfaces userspaceFilter=selected-ifindexes")
	}

	deadline := time.Now().Add(timeout)
	buffer := make([]byte, 65535)
	hostname, _ := os.Hostname()
	neighborIndexes := make(map[string]int)
	validFrames := 0
	var idleAnchor time.Time
	stopReason := "max-timeout"
	distinctTarget, bondTargetApplies := requiredDistinctBondLeaves(candidates)
	effectiveIdleTimeout := idleTimeout
	if automatic && bondTargetApplies {
		// lldp-new-2 uses interface coverage to finish Bond collection. Keep
		// that flow, but do not allow the generic idle timer to finish a dual
		// Leaf scan after only the first switch has advertised.
		effectiveIdleTimeout = 0
	}
	log.Printf("[LLDP-AGENT] WAIT scope=%s protocol=0x%04x timeout=%s idleTimeout=%s effectiveIdleTimeout=%s collectionMode=%s maxUniqueNeighbors=%d automaticDistinctBondLeafTarget=%d", collectionScope(automatic), ethernetProtocolLLDP, timeout, idleTimeout, effectiveIdleTimeout, collectionMode(count, effectiveIdleTimeout), count, distinctTarget)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining, idleExpired := nextReceiveWait(time.Now(), deadline, idleAnchor, effectiveIdleTimeout)
		if idleExpired {
			stopReason = "idle-timeout"
			break
		}
		if remaining <= 0 {
			break
		}
		poll := remaining
		if poll > time.Second {
			poll = time.Second
		}
		_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{
			Sec: int64(poll / time.Second), Usec: int64((poll % time.Second) / time.Microsecond),
		})
		bytesRead, address, err := unix.Recvfrom(fd, buffer, 0)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
				continue
			}
			return nil, fmt.Errorf("receive LLDP frame: %w", err)
		}
		link, ok := address.(*unix.SockaddrLinklayer)
		if !ok {
			log.Printf("[LLDP-AGENT] IGNORE frame reason=unexpected-socket-address")
			continue
		}
		if link.Pkttype == unix.PACKET_OUTGOING {
			log.Printf("[LLDP-AGENT] IGNORE frame bytes=%d reason=locally-originated", bytesRead)
			continue
		}
		selection, selected := candidates[link.Ifindex]
		if !selected {
			iface, lookupErr := net.InterfaceByIndex(link.Ifindex)
			if lookupErr != nil {
				log.Printf("[LLDP-AGENT] IGNORE frame ifindex=%d bytes=%d reason=unknown-interface error=%v", link.Ifindex, bytesRead, lookupErr)
			} else {
				log.Printf("[LLDP-AGENT] IGNORE frame interface=%s ifindex=%d bytes=%d reason=not-selected-by-link-physical-bond-policy", iface.Name, link.Ifindex, bytesRead)
			}
			continue
		}
		log.Printf("[LLDP-AGENT] MAP ifindex=%d interface=%s kind=%s bondMaster=%q", link.Ifindex, selection.Name, selection.Kind, selection.BondName)
		log.Printf("[LLDP-AGENT] RECEIVE frame interface=%s ifindex=%d bytes=%d packetType=%d", selection.Name, link.Ifindex, bytesRead, link.Pkttype)
		neighbor, ok := parseLLDPFrame(buffer[:bytesRead], selection.Name)
		if !ok {
			log.Printf("[LLDP-AGENT] IGNORE frame interface=%s reason=invalid-LLDP", selection.Name)
			continue
		}
		neighbor.ReceivedAt = time.Now()
		applyInterfaceSelection(&neighbor, selection)
		neighbor.LooksLikeLocalHost = sameHostName(neighbor.SystemName, hostname)
		validFrames++
		if neighbor.LooksLikeLocalHost {
			log.Printf("[LLDP-AGENT] WARNING neighbor systemName=%q resembles local hostname=%q; upper-switch selection will reject it", neighbor.SystemName, hostname)
		}
		identity := neighborIdentity(neighbor)
		if index, exists := neighborIndexes[identity]; exists {
			result[index] = neighbor
			log.Printf("[LLDP-AGENT] LEAF UPDATE duplicate=true interface=%s chassisID=%q portID=%q uniqueNeighbors=%d validFrames=%d idleTimerReset=false", neighbor.LocalInterface, neighbor.ChassisID, neighbor.PortID, len(result), validFrames)
			continue
		}
		distinctBefore := distinctLeafCount(result)
		neighborIndexes[identity] = len(result)
		result = append(result, neighbor)
		distinctAfter := distinctLeafCount(result)
		newDistinctLeaf := distinctAfter > distinctBefore
		now := time.Now()
		idleReset := false
		if automatic {
			if newDistinctLeaf && effectiveIdleTimeout > 0 {
				idleAnchor = now
				idleReset = true
			}
		} else {
			covered, expected := selectedInterfaceCoverage(candidates, result)
			coverageWasComplete := !idleAnchor.IsZero()
			coverageComplete := covered == expected
			if coverageComplete && (!coverageWasComplete || newDistinctLeaf) && effectiveIdleTimeout > 0 {
				idleAnchor = now
				idleReset = true
			}
			log.Printf("[LLDP-AGENT] EXPLICIT INTERFACE COVERAGE coveredInterfaces=%d expectedInterfaces=%d complete=%t idleTimerStarted=%t", covered, expected, coverageComplete, !idleAnchor.IsZero())
		}
		log.Printf("[LLDP-AGENT] LEAF FOUND duplicate=false newDistinctChassis=%t interface=%s chassisID=%q portID=%q uniqueNeighbors=%d distinctLeaves=%d validFrames=%d idleTimerReset=%t", newDistinctLeaf, neighbor.LocalInterface, neighbor.ChassisID, neighbor.PortID, len(result), distinctAfter, validFrames, idleReset)

		covered, expected, applicable := bondInterfaceCoverage(candidates, result)
		if applicable {
			log.Printf("[LLDP-AGENT] BOND COVERAGE coveredInterfaces=%d expectedInterfaces=%d complete=%t", covered, expected, covered == expected)
			if automatic {
				distinct := distinctLeafCount(result)
				complete := bondLeafCollectionComplete(candidates, result)
				log.Printf("[LLDP-AGENT] AUTOMATIC BOND LEAF TARGET distinctLeaves=%d requiredDistinctLeaves=%d complete=%t", distinct, distinctTarget, complete)
				if complete {
					stopReason = "bond-distinct-leaf-coverage-complete"
					break
				}
			} else {
				log.Printf("[LLDP-AGENT] EXPLICIT BOND LEAF INVENTORY distinctLeaves=%d limit=none", distinctLeafCount(result))
			}
		}
		if count > 0 && len(result) >= count {
			stopReason = "unique-neighbor-limit-reached"
			break
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("LLDP receive timeout after %s on interfaces %v: no valid inbound EtherType 0x88cc frame", timeout, allowedNames)
	}
	if automatic {
		if err := validateBondLeafCollection(candidates, result, timeout, allowedNames); err != nil {
			return nil, err
		}
	} else {
		covered, expected := selectedInterfaceCoverage(candidates, result)
		log.Printf("[LLDP-AGENT] EXPLICIT COLLECTION FINAL coveredInterfaces=%d expectedInterfaces=%d complete=%t distinctLeaves=%d", covered, expected, covered == expected, distinctLeafCount(result))
	}
	sortNeighbors(result)
	log.Printf("[LLDP-AGENT] COLLECTION COMPLETE reason=%s collectionMode=%s validFrames=%d uniqueNeighbors=%d distinctLeaves=%d maxUniqueNeighbors=%d", stopReason, collectionMode(count, effectiveIdleTimeout), validFrames, len(result), distinctLeafCount(result), count)
	return result, nil
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

func collectionMode(count int, idleTimeout time.Duration) string {
	if count > 0 {
		return "until-limit"
	}
	if idleTimeout > 0 {
		return "until-idle"
	}
	return "full-window"
}

func nextReceiveWait(now, deadline, lastNewNeighbor time.Time, idleTimeout time.Duration) (time.Duration, bool) {
	remaining := deadline.Sub(now)
	if remaining <= 0 || idleTimeout <= 0 || lastNewNeighbor.IsZero() {
		return remaining, false
	}
	idleRemaining := lastNewNeighbor.Add(idleTimeout).Sub(now)
	if idleRemaining <= 0 {
		return 0, true
	}
	if idleRemaining < remaining {
		return idleRemaining, false
	}
	return remaining, false
}

func bondInterfaceCoverage(candidates map[int]interfaceSelection, neighbors []lldpNeighbor) (covered, expected int, applicable bool) {
	if len(candidates) == 0 {
		return 0, 0, false
	}
	expectedNames := make(map[string]struct{}, len(candidates))
	for _, selection := range candidates {
		if selection.Kind != "bond-slave" || selection.BondName == "" {
			return 0, 0, false
		}
		expectedNames[selection.Name] = struct{}{}
	}
	coveredNames := coveredInterfaces(expectedNames, neighbors)
	return len(coveredNames), len(expectedNames), true
}

func selectedInterfaceCoverage(candidates map[int]interfaceSelection, neighbors []lldpNeighbor) (covered, expected int) {
	expectedNames := make(map[string]struct{}, len(candidates))
	for _, selection := range candidates {
		expectedNames[selection.Name] = struct{}{}
	}
	return len(coveredInterfaces(expectedNames, neighbors)), len(expectedNames)
}

func coveredInterfaces(expectedNames map[string]struct{}, neighbors []lldpNeighbor) map[string]struct{} {
	coveredNames := make(map[string]struct{}, len(expectedNames))
	for _, neighbor := range neighbors {
		if neighbor.LooksLikeLocalHost || leafChassisIdentity(neighbor) == "" {
			continue
		}
		if _, expectedInterface := expectedNames[neighbor.LocalInterface]; expectedInterface {
			coveredNames[neighbor.LocalInterface] = struct{}{}
		}
	}
	return coveredNames
}

// requiredDistinctBondLeaves describes the operational topology expected from
// the selected Bond. active-backup exposes only the active path; every other
// mode expects two distinct upstream Leaf switches when the Bond has at least
// two slaves.
func requiredDistinctBondLeaves(candidates map[int]interfaceSelection) (int, bool) {
	if len(candidates) == 0 {
		return 0, false
	}
	bondName, bondMode := "", ""
	declaredSlaves := map[string]struct{}{}
	for _, selection := range candidates {
		if selection.Kind != "bond-slave" || selection.BondName == "" {
			return 0, false
		}
		if bondName == "" {
			bondName, bondMode = selection.BondName, selection.BondMode
		}
		if selection.BondName != bondName || selection.BondMode != bondMode {
			return 0, false
		}
		for _, slave := range selection.BondSlaves {
			declaredSlaves[slave] = struct{}{}
		}
	}
	if bondMode == "active-backup" {
		return 1, true
	}
	available := len(declaredSlaves)
	if available == 0 {
		available = len(candidates)
	}
	if available >= 2 {
		return 2, true
	}
	return 1, true
}

func bondLeafCollectionComplete(candidates map[int]interfaceSelection, neighbors []lldpNeighbor) bool {
	target, applicable := requiredDistinctBondLeaves(candidates)
	if !applicable || distinctLeafCount(neighbors) < target {
		return false
	}
	covered, _, _ := bondInterfaceCoverage(candidates, neighbors)
	requiredInterfaces := target
	if requiredInterfaces > len(candidates) {
		requiredInterfaces = len(candidates)
	}
	return covered >= requiredInterfaces
}

func validateBondLeafCollection(candidates map[int]interfaceSelection, neighbors []lldpNeighbor, timeout time.Duration, interfaces []string) error {
	target, applicable := requiredDistinctBondLeaves(candidates)
	distinct := distinctLeafCount(neighbors)
	if applicable && distinct < target {
		return fmt.Errorf("LLDP Bond collection incomplete after %s on interfaces %v: got %d distinct Leaf chassis, require %d; keeping previously persisted Node topology", timeout, interfaces, distinct, target)
	}
	return nil
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
