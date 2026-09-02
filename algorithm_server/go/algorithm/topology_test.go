package algorithm

import (
	"os"
	"testing"
)

const unicomTopologyFixture = `
version: unicom-huailai-v1
scopes:
  regions:
    - id: CN-NORTH
      name: 华北
  locations:
    - id: HB-HL
      name: 怀来
      regionId: CN-NORTH
  dataCenters:
    - id: HB-HL-DC1
      locationId: HB-HL
  rooms:
    - id: HB-HL-DC1-102
      dataCenterId: HB-HL-DC1
borderDomains:
  HB-HL-DC1-102-BORDER-DOMAIN-01:
    roomId: HB-HL-DC1-102
    mode: exact-set
    members:
      - HB-HL-DC1-102-C03-02U-LTY-CSQ-BORDER-SW01-ZTE9904X
      - HB-HL-DC1-102-D03-02U-LTY-CSQ-BORDER-SW02-ZTE9904X
topology:
  HB-HL-DC1-102:
    HB-HL-DC1-102-C03-44U-LTY-CSQ-LEAF-SW01-ZTE5960X:
      SPINE: {}
      BORDER:
        HB-HL-DC1-102-C03-02U-LTY-CSQ-BORDER-SW01-ZTE9904X:
          local_port: cgei-0/1/1/51
          peer_port: cgei-0/3/0/9
        HB-HL-DC1-102-D03-02U-LTY-CSQ-BORDER-SW02-ZTE9904X:
          local_port: cgei-0/1/1/53
          peer_port: cgei-0/3/0/9
      LEAF:
        HB-HL-DC1-102-C04-44U-LTY-CSQ-LEAF-SW02-ZTE5960X:
          local_port: cgei-0/1/1/54
          peer_port: cgei-0/1/1/54
    HB-HL-DC1-102-C04-44U-LTY-CSQ-LEAF-SW02-ZTE5960X:
      SPINE: {}
      BORDER:
        HB-HL-DC1-102-C03-02U-LTY-CSQ-BORDER-SW01-ZTE9904X:
          local_port: cgei-0/1/1/51
          peer_port: cgei-0/3/0/10
        HB-HL-DC1-102-D03-02U-LTY-CSQ-BORDER-SW02-ZTE9904X:
          local_port: cgei-0/1/1/53
          peer_port: cgei-0/3/0/10
      LEAF:
        HB-HL-DC1-102-C03-44U-LTY-CSQ-LEAF-SW01-ZTE5960X:
          local_port: cgei-0/1/1/54
          peer_port: cgei-0/1/1/54
`

func TestUnicomTopologyResolvesLeafOnlyNodesIntoBorderDomain(t *testing.T) {
	cache, err := loadTopologyCache("", []byte(unicomTopologyFixture))
	if err != nil {
		t.Fatalf("load Unicom topology: %v", err)
	}
	leaves := []string{
		"HB-HL-DC1-102-C03-44U-LTY-CSQ-LEAF-SW01-ZTE5960X",
		"HB-HL-DC1-102-C04-44U-LTY-CSQ-LEAF-SW02-ZTE5960X",
	}
	nodes := make([]map[string]any, 0, len(leaves))
	for index, leaf := range leaves {
		nodes = append(nodes, map[string]any{
			"nodeName": "worker-" + string(rune('1'+index)),
			"nodeUID":  "uid-" + string(rune('1'+index)),
			"topology": map[string]any{"leafSwitchId": leaf, "switchId": leaf},
		})
	}
	resolved, warnings, err := cache.resolve(staticSnapshot{Nodes: nodes})
	if err != nil {
		t.Fatalf("resolve Leaf-only snapshot: %v", err)
	}
	if len(warnings) != 0 || len(resolved.Nodes) != 2 {
		t.Fatalf("resolved=%d warnings=%v", len(resolved.Nodes), warnings)
	}
	for _, node := range resolved.Nodes {
		topology := node["topology"].(map[string]any)
		if topology["regionId"] != "CN-NORTH" || topology["locationId"] != "HB-HL" || topology["roomId"] != "HB-HL-DC1-102" {
			t.Fatalf("unexpected upper topology: %#v", topology)
		}
		if topology["borderDomainId"] != "HB-HL-DC1-102-BORDER-DOMAIN-01" {
			t.Fatalf("unexpected Border Domain: %#v", topology)
		}
		if topology["spineDomainId"] != "" {
			t.Fatalf("empty SPINE must remain empty: %#v", topology)
		}
	}
}

func TestRepositoryTopologyConfigurationsAreValid(t *testing.T) {
	for _, path := range []string{
		"../../../config/topology/unicom-kind-topology.yaml",
		"../../../config/topology/unicom-huailai-102-sample.yaml",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if _, err := loadTopologyCache("", raw); err != nil {
			t.Fatalf("validate %s: %v", path, err)
		}
	}
}
