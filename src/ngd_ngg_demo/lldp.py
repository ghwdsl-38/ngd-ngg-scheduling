from __future__ import annotations

import socket
import time
from dataclasses import dataclass


ETH_P_LLDP = 0x88CC


@dataclass(frozen=True)
class LLDPNeighbor:
    local_interface: str
    chassis_id: str
    port_id: str
    system_name: str
    ttl_seconds: int

    @property
    def switch_id(self) -> str:
        return self.system_name or self.chassis_id


def _decode_identifier(value: bytes) -> str:
    if not value:
        return ""
    subtype, identifier = value[0], value[1:]
    if subtype == 4 and len(identifier) == 6:
        return ":".join(f"{part:02x}" for part in identifier)
    return identifier.decode("utf-8", errors="replace").strip()


def parse_lldp_frame(frame: bytes, local_interface: str = "") -> LLDPNeighbor | None:
    """Parse the mandatory LLDP TLVs from an Ethernet frame or raw LLDP payload."""
    if len(frame) >= 14 and int.from_bytes(frame[12:14], "big") == ETH_P_LLDP:
        payload = frame[14:]
    else:
        payload = frame

    chassis_id = ""
    port_id = ""
    system_name = ""
    ttl_seconds = 0
    offset = 0
    while offset + 2 <= len(payload):
        header = int.from_bytes(payload[offset : offset + 2], "big")
        offset += 2
        tlv_type = header >> 9
        length = header & 0x1FF
        if offset + length > len(payload):
            return None
        value = payload[offset : offset + length]
        offset += length
        if tlv_type == 0:
            break
        if tlv_type == 1:
            chassis_id = _decode_identifier(value)
        elif tlv_type == 2:
            port_id = _decode_identifier(value)
        elif tlv_type == 3 and len(value) == 2:
            ttl_seconds = int.from_bytes(value, "big")
        elif tlv_type == 5:
            system_name = value.decode("utf-8", errors="replace").strip()

    if not chassis_id or not port_id:
        return None
    return LLDPNeighbor(
        local_interface=local_interface,
        chassis_id=chassis_id,
        port_id=port_id,
        system_name=system_name,
        ttl_seconds=ttl_seconds,
    )


def receive_neighbor(
    interfaces: set[str] | None = None,
    timeout_seconds: float = 35.0,
) -> LLDPNeighbor | None:
    """Listen for one LLDP frame. Real mode requires host networking and CAP_NET_RAW."""
    deadline = time.monotonic() + timeout_seconds
    with socket.socket(
        socket.AF_PACKET, socket.SOCK_RAW, socket.htons(ETH_P_LLDP)
    ) as listener:
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return None
            listener.settimeout(remaining)
            try:
                frame, address = listener.recvfrom(65535)
            except TimeoutError:
                return None
            interface = str(address[0]) if address else ""
            if interfaces and interface not in interfaces:
                continue
            neighbor = parse_lldp_frame(frame, interface)
            if neighbor:
                return neighbor
