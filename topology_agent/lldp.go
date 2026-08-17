package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const ethernetProtocolLLDP = 0x88cc

type lldpNeighbor struct {
	LocalInterface string
	ChassisID      string
	PortID         string
	SystemName     string
	TTLSeconds     uint16
}

func (n lldpNeighbor) switchID() string {
	if n.SystemName != "" {
		return n.SystemName
	}
	return n.ChassisID
}

func parseLLDPFrame(frame []byte, localInterface string) (lldpNeighbor, bool) {
	payload := frame
	if len(frame) >= 14 && binary.BigEndian.Uint16(frame[12:14]) == ethernetProtocolLLDP {
		payload = frame[14:]
	}
	result := lldpNeighbor{LocalInterface: localInterface}
	for offset := 0; offset+2 <= len(payload); {
		header := binary.BigEndian.Uint16(payload[offset : offset+2])
		offset += 2
		kind, length := header>>9, int(header&0x1ff)
		if offset+length > len(payload) {
			return lldpNeighbor{}, false
		}
		value := payload[offset : offset+length]
		offset += length
		switch kind {
		case 0:
			offset = len(payload)
		case 1:
			result.ChassisID = decodeIdentifier(value)
		case 2:
			result.PortID = decodeIdentifier(value)
		case 3:
			if len(value) == 2 {
				result.TTLSeconds = binary.BigEndian.Uint16(value)
			}
		case 5:
			result.SystemName = strings.TrimSpace(string(value))
		}
	}
	return result, result.ChassisID != "" && result.PortID != ""
}

func decodeIdentifier(value []byte) string {
	if len(value) < 1 {
		return ""
	}
	if value[0] == 4 && len(value) == 7 {
		return net.HardwareAddr(value[1:]).String()
	}
	return strings.TrimSpace(string(value[1:]))
}

func receiveLLDP(allowed map[string]struct{}, timeout time.Duration) (lldpNeighbor, error) {
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(htons(ethernetProtocolLLDP)))
	if err != nil {
		return lldpNeighbor{}, fmt.Errorf("open LLDP raw socket: %w", err)
	}
	defer unix.Close(fd)
	deadline := time.Now().Add(timeout)
	buffer := make([]byte, 65535)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: int64(remaining / time.Second), Usec: int64((remaining % time.Second) / time.Microsecond)})
		count, address, err := unix.Recvfrom(fd, buffer, 0)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				return lldpNeighbor{}, fmt.Errorf("LLDP listen timeout")
			}
			return lldpNeighbor{}, err
		}
		link, ok := address.(*unix.SockaddrLinklayer)
		if !ok {
			continue
		}
		iface, err := net.InterfaceByIndex(link.Ifindex)
		if err != nil {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[iface.Name]; !ok {
				continue
			}
		}
		if neighbor, ok := parseLLDPFrame(buffer[:count], iface.Name); ok {
			return neighbor, nil
		}
	}
	return lldpNeighbor{}, fmt.Errorf("LLDP listen timeout")
}

func htons(value uint16) uint16 { return value<<8 | value>>8 }
