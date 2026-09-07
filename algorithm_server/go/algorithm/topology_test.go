package algorithm

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
		if topology["leafDomainId"] == "" {
			t.Fatalf("Leaf Domain must be resolved: %#v", topology)
		}
	}
	first := resolved.Nodes[0]["topology"].(map[string]any)["leafDomainId"]
	second := resolved.Nodes[1]["topology"].(map[string]any)["leafDomainId"]
	if first != second {
		t.Fatalf("peer Leaves must share one Leaf Domain: first=%v second=%v", first, second)
	}

	dualNode := map[string]any{
		"nodeName": "worker-dual", "nodeUID": "uid-dual",
		"topology": map[string]any{"leafSwitchIds": []string{leaves[1], leaves[0]}},
	}
	dualResolved, warnings, err := cache.resolve(staticSnapshot{Nodes: []map[string]any{dualNode}})
	if err != nil || len(warnings) != 0 || len(dualResolved.Nodes) != 1 {
		t.Fatalf("resolve dual-Leaf Node: nodes=%d warnings=%v err=%v", len(dualResolved.Nodes), warnings, err)
	}
	dualTopology := dualResolved.Nodes[0]["topology"].(map[string]any)
	if dualTopology["leafDomainId"] != first {
		t.Fatalf("dual-Leaf Node domain=%v, want %v", dualTopology["leafDomainId"], first)
	}
}

func TestRepositoryTopologyConfigurationsAreValid(t *testing.T) {
	for _, path := range []string{
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

func TestDeployedAlgorithmConfigMapContainsValidTopology(t *testing.T) {
	raw, err := os.ReadFile("../../../config/manager/algorithm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	topologyData := ""
	for {
		var manifest struct {
			Data map[string]string `yaml:"data"`
		}
		if err := decoder.Decode(&manifest); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode Algorithm manifest: %v", err)
		}
		if candidate := manifest.Data["topology.yaml"]; candidate != "" {
			topologyData = candidate
			break
		}
	}
	if topologyData == "" {
		t.Fatal("Algorithm ConfigMap does not contain data.topology.yaml")
	}
	if _, err := loadTopologyCache("", []byte(topologyData)); err != nil {
		t.Fatalf("validate deployed Algorithm topology: %v", err)
	}
}

func TestDemandTopologyLabelsMapPhysicalSwitchesAndFallbackFromMissingSpine(t *testing.T) {
	cache, err := loadTopologyCache("", []byte(unicomTopologyFixture))
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"ngd": map[string]any{"topologyLabels": map[string]any{
		"topology.kubernetes.io/data-center":   "HB-HL-DC1",
		"topology.kubernetes.io/room":          "HB-HL-DC1-102",
		"topology.kubernetes.io/border-switch": "requiredSame",
		"topology.kubernetes.io/spine-switch":  "spine-01",
		"topology.kubernetes.io/leaf-switch":   "requiredSame",
	}}}
	constraints, warnings, err := cache.resolveDemandTopologyLabels(request)
	if err != nil {
		t.Fatalf("resolve topologyLabels: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "SPINE_NOT_FOUND_FALLBACK") {
		t.Fatalf("warnings=%v, want missing-Spine fallback", warnings)
	}
	for key, want := range map[string]string{
		"dataCenter": "HB-HL-DC1", "room": "HB-HL-DC1-102",
		"borderDomain": requiredSame, "leafDomain": requiredSame,
	} {
		if got := stringValue(constraints[key]); got != want {
			t.Fatalf("constraint %s=%q, want %q; all=%v", key, got, want, constraints)
		}
	}
	if _, found := constraints["spineDomain"]; found {
		t.Fatalf("missing Spine must not remain as an effective constraint: %v", constraints)
	}

	border := "HB-HL-DC1-102-C03-02U-LTY-CSQ-BORDER-SW01-ZTE9904X"
	leaf := "HB-HL-DC1-102-C03-44U-LTY-CSQ-LEAF-SW01-ZTE5960X"
	constraints, warnings, err = cache.resolveDemandTopologyLabels(map[string]any{"ngd": map[string]any{"topologyLabels": map[string]any{
		"topology.kubernetes.io/border-switch": border,
		"topology.kubernetes.io/leaf-switch":   leaf,
	}}})
	if err != nil || len(warnings) != 0 {
		t.Fatalf("map physical switches: constraints=%v warnings=%v err=%v", constraints, warnings, err)
	}
	if constraints["borderDomain"] != "HB-HL-DC1-102-BORDER-DOMAIN-01" {
		t.Fatalf("physical Border was not mapped to its logical domain: %v", constraints)
	}
	if constraints["leafDomain"] == leaf {
		t.Fatalf("peer Leaf must map to the shared logical Leaf domain, got %v", constraints)
	}
}

func TestDemandTopologyLabelsUseConfiguredSpineAndRejectUnsupportedLevel(t *testing.T) {
	withSpine := strings.ReplaceAll(unicomTopologyFixture, "SPINE: {}", `SPINE:
        SPINE-01:
          local_port: cgei-0/1/1/49
          peer_port: cgei-0/2/0/1`)
	cache, err := loadTopologyCache("", []byte(withSpine))
	if err != nil {
		t.Fatal(err)
	}
	constraints, warnings, err := cache.resolveDemandTopologyLabels(map[string]any{"ngd": map[string]any{"topologyLabels": map[string]any{
		"topology.kubernetes.io/spine-switch": "SPINE-01",
	}}})
	if err != nil || len(warnings) != 0 || stringValue(constraints["spineDomain"]) == "" {
		t.Fatalf("configured Spine must resolve without fallback: constraints=%v warnings=%v err=%v", constraints, warnings, err)
	}

	_, _, err = cache.resolveDemandTopologyLabels(map[string]any{"ngd": map[string]any{"topologyLabels": map[string]any{
		"topology.kubernetes.io/access-switch": "10.0.1.10",
	}}})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported access-switch must be rejected, got %v", err)
	}
}
