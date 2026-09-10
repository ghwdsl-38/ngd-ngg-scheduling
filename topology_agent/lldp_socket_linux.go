package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

var lldpMulticastAddresses = [][6]byte{
	{0x01, 0x80, 0xc2, 0x00, 0x00, 0x00},
	{0x01, 0x80, 0xc2, 0x00, 0x00, 0x03},
	{0x01, 0x80, 0xc2, 0x00, 0x00, 0x0e},
}

func receiveLLDPOnHost(ctx context.Context, selections map[string]interfaceSelection, bindSingle bool, timeout time.Duration, count int) (result []lldpNeighbor, returnErr error) {
	started := time.Now()
	allowed := make(map[string]struct{}, len(selections))
	candidates := make(map[int]interfaceSelection, len(selections))
	captureInterfaces := make(map[string]*net.Interface, len(selections))
	for name, selection := range selections {
		allowed[name] = struct{}{}
		iface, err := net.InterfaceByName(name)
		if err != nil {
			return nil, fmt.Errorf("resolve selected interface %q: %w", name, err)
		}
		selection.Index = iface.Index
		candidates[iface.Index] = selection
		captureInterfaces[name] = iface
	}
	allowedNames := sortedInterfaceSet(allowed)
	log.Printf("[LLDP-AGENT] ENTER receiveLLDP interfaces=%v bindSingle=%t timeout=%s count=%d", allowedNames, bindSingle, timeout, count)
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
	for _, name := range allowedNames {
		if err := subscribeLLDPMulticast(fd, captureInterfaces[name]); err != nil {
			return nil, err
		}
	}

	// Bind one explicit interface, including a Bond master.
	if bindSingle && len(selections) == 1 {
		iface := captureInterfaces[allowedNames[0]]
		address := &unix.SockaddrLinklayer{Protocol: htons(ethernetProtocolLLDP), Ifindex: iface.Index}
		log.Printf("[LLDP-AGENT] CALL unix.Bind interface=%s ifindex=%d", iface.Name, iface.Index)
		if err := unix.Bind(fd, address); err != nil {
			return nil, fmt.Errorf("bind LLDP socket to %q: %w", iface.Name, err)
		}
		log.Printf("[LLDP-AGENT] RETURN unix.Bind status=success")
	} else {
		log.Printf("[LLDP-AGENT] SKIP unix.Bind reason=auto-or-multiple-interface-scope kernelScope=all userspaceFilter=selected-ifindexes")
	}

	deadline := time.Now().Add(timeout)
	buffer := make([]byte, 65535)
	hostname, _ := os.Hostname()
	neighborIndexes := make(map[string]int)
	validFrames := 0
	stopReason := "max-timeout"
	log.Printf("[LLDP-AGENT] WAIT protocol=0x%04x timeout=%s collectionMode=%s maxUniqueNeighbors=%d", ethernetProtocolLLDP, timeout, collectionMode(count), count)
	for count == 0 || len(result) < count {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := time.Until(deadline)
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
		neighborIndexes[identity] = len(result)
		result = append(result, neighbor)
		log.Printf("[LLDP-AGENT] LEAF FOUND duplicate=false interface=%s chassisID=%q portID=%q uniqueNeighbors=%d distinctLeaves=%d validFrames=%d", neighbor.LocalInterface, neighbor.ChassisID, neighbor.PortID, len(result), distinctLeafCount(result), validFrames)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("LLDP receive timeout after %s on interfaces %v: no valid inbound EtherType 0x88cc frame", timeout, allowedNames)
	}
	if count > 0 && len(result) >= count {
		stopReason = "unique-neighbor-limit-reached"
	}
	sortNeighbors(result)
	log.Printf("[LLDP-AGENT] COLLECTION COMPLETE reason=%s collectionMode=%s validFrames=%d uniqueNeighbors=%d distinctLeaves=%d maxUniqueNeighbors=%d", stopReason, collectionMode(count), validFrames, len(result), distinctLeafCount(result), count)
	return result, nil
}

func subscribeLLDPMulticast(fd int, iface *net.Interface) error {
	for _, address := range lldpMulticastAddresses {
		request := &unix.PacketMreq{
			Ifindex: int32(iface.Index),
			Type:    unix.PACKET_MR_MULTICAST,
			Alen:    uint16(len(address)),
		}
		copy(request.Address[:], address[:])
		mac := net.HardwareAddr(address[:]).String()
		log.Printf("[LLDP-AGENT] CALL unix.SetsockoptPacketMreq interface=%s ifindex=%d multicast=%s", iface.Name, iface.Index, mac)
		if err := unix.SetsockoptPacketMreq(fd, unix.SOL_PACKET, unix.PACKET_ADD_MEMBERSHIP, request); err != nil {
			return fmt.Errorf("subscribe interface %q to LLDP multicast %s: %w", iface.Name, mac, err)
		}
		log.Printf("[LLDP-AGENT] RETURN unix.SetsockoptPacketMreq status=success interface=%s multicast=%s", iface.Name, mac)
	}
	return nil
}
