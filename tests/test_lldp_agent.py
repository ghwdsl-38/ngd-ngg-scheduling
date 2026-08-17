from __future__ import annotations

import unittest

from ngd_ngg_demo.lldp import parse_lldp_frame
from ngd_ngg_demo.lldp_agent import simulated_observation


def tlv(tlv_type: int, value: bytes) -> bytes:
    return ((tlv_type << 9) | len(value)).to_bytes(2, "big") + value


class LLDPTests(unittest.TestCase):
    def test_parses_switch_and_port_from_lldp_frame(self) -> None:
        destination = bytes.fromhex("0180c200000e")
        source = bytes.fromhex("001122334455")
        ethernet = destination + source + bytes.fromhex("88cc")
        payload = b"".join(
            [
                tlv(1, b"\x04" + bytes.fromhex("aabbccddeeff")),
                tlv(2, b"\x05Ethernet1/9"),
                tlv(3, (120).to_bytes(2, "big")),
                tlv(5, b"switch-c"),
                tlv(0, b""),
            ]
        )
        neighbor = parse_lldp_frame(ethernet + payload, "eno1")
        self.assertIsNotNone(neighbor)
        assert neighbor is not None
        self.assertEqual(neighbor.switch_id, "switch-c")
        self.assertEqual(neighbor.port_id, "Ethernet1/9")
        self.assertEqual(neighbor.local_interface, "eno1")
        self.assertEqual(neighbor.ttl_seconds, 120)

    def test_simulated_mode_uses_same_nnt_status_contract(self) -> None:
        node = {
            "metadata": {
                "labels": {
                    "topology.demo.ngg.io/switch": "switch-a",
                    "topology.demo.ngg.io/core-switch": "core-0",
                    "topology.demo.ngg.io/bandwidth-gbps": "20",
                    "topology.demo.ngg.io/latency-ms": "1.5",
                    "topology.demo.ngg.io/local-interface": "eth0"
                },
                "annotations": {
                    "topology.demo.ngg.io/remote-port": "Ethernet1/1"
                },
            }
        }
        result = simulated_observation(node)
        self.assertEqual(result["switchId"], "switch-a")
        self.assertEqual(result["bandwidthGbps"], 20.0)
        self.assertEqual(result["latencyMillis"], 1.5)
        self.assertEqual(result["source"], "SimulatedLLDP")


if __name__ == "__main__":
    unittest.main()
