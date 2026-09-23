package topology

import (
	"bytes"
	"os"
	"testing"
)

const mixedSource = `ROOM-DC1-101:
  LEAF-1:
    SPINE: {SPINE-1: {local_port: p1, peer_port: p2}}
    BORDER: {}
    LEAF: {LEAF-2: {local_port: p3, peer_port: p4}}
  LEAF-2:
    SPINE: {SPINE-1: {local_port: p1, peer_port: p2}}
    BORDER: {}
    LEAF: {LEAF-1: {local_port: p3, peer_port: p4}}
ROOM-DC1-102:
  LEAF-3:
    SPINE: {}
    BORDER: {BORDER-1: {local_port: p1, peer_port: p2}}
    LEAF: {}
`

func TestMixedSourceUsesUplinkCompatibility(t *testing.T) {
	cache, err := LoadSource("", []byte(mixedSource))
	if err != nil {
		t.Fatal(err)
	}
	if cache.Mode() != ModeUplinkCompatible {
		t.Fatalf("mode=%s", cache.Mode())
	}
	status := cache.Status()
	if status["spineOnlyLeafCount"] != 2 || status["borderOnlyLeafCount"] != 1 {
		t.Fatalf("status=%v", status)
	}
	for _, key := range []string{"topology.kubernetes.io/spine-switch", "topology.kubernetes.io/border-switch"} {
		constraints, _, err := cache.ResolveDemandTopologyLabels(map[string]any{"ngd": map[string]any{"topologyLabels": map[string]any{key: RequiredSame}}})
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if constraints["uplinkDomain"] != RequiredSame {
			t.Fatalf("%s constraints=%v", key, constraints)
		}
	}
}

func TestLayeredSourceKeepsSpineAndBorderLevels(t *testing.T) {
	raw := `ROOM-DC1-101:
  LEAF-1:
    SPINE: {SPINE-1: {}}
    BORDER: {BORDER-1: {}}
    LEAF: {}
`
	cache, err := LoadSource("", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if cache.Mode() != ModeLayered {
		t.Fatalf("mode=%s", cache.Mode())
	}
	constraints, _, err := cache.ResolveDemandTopologyLabels(map[string]any{"ngd": map[string]any{"topologyLabels": map[string]any{"topology.kubernetes.io/spine-switch": RequiredSame, "topology.kubernetes.io/border-switch": RequiredSame}}})
	if err != nil {
		t.Fatal(err)
	}
	if constraints["spineDomain"] != RequiredSame || constraints["borderDomain"] != RequiredSame {
		t.Fatalf("constraints=%v", constraints)
	}
}

func TestResolveDualLeafAndRejectUnknownSwitch(t *testing.T) {
	cache, err := LoadSource("", []byte(mixedSource))
	if err != nil {
		t.Fatal(err)
	}
	nodes, warnings, err := cache.ResolveNodes([]map[string]any{{"nodeName": "n1", "nodeUID": "u1", "topology": map[string]any{"leafSwitchIds": []string{"LEAF-2", "LEAF-1"}}}})
	if err != nil || len(warnings) != 0 || len(nodes) != 1 {
		t.Fatalf("nodes=%d warnings=%v err=%v", len(nodes), warnings, err)
	}
	topo := nodes[0]["topology"].(map[string]any)
	if topo["uplinkDomainId"] == "" || topo["topologyMode"] != ModeUplinkCompatible {
		t.Fatalf("topology=%v", topo)
	}
	_, _, err = cache.ResolveDemandTopologyLabels(map[string]any{"ngd": map[string]any{"topologyLabels": map[string]any{"topology.kubernetes.io/spine-switch": "MISSING"}}})
	if err == nil {
		t.Fatal("missing concrete switch must fail")
	}
}

func TestCompatibleConcreteSwitchesStayStrict(t *testing.T) {
	cache, err := LoadSource("", []byte(mixedSource))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"topology.kubernetes.io/spine-switch":  "SPINE-1",
		"topology.kubernetes.io/border-switch": "BORDER-1",
	} {
		constraints, _, err := cache.ResolveDemandTopologyLabels(map[string]any{"ngd": map[string]any{"topologyLabels": map[string]any{key: value}}})
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if constraints["uplinkDomain"] != RequiredSame {
			t.Fatalf("%s constraints=%v", key, constraints)
		}
	}
	_, _, err = cache.ResolveDemandTopologyLabels(map[string]any{"ngd": map[string]any{"topologyLabels": map[string]any{
		"topology.kubernetes.io/spine-switch":  "SPINE-1",
		"topology.kubernetes.io/border-switch": "BORDER-1",
	}}})
	if err == nil {
		t.Fatal("concrete Spine and Border on different Leaves must conflict")
	}
	constraints, _, err := cache.ResolveDemandTopologyLabels(map[string]any{"ngd": map[string]any{"topologyLabels": map[string]any{
		"topology.kubernetes.io/spine-switch":  RequiredSame,
		"topology.kubernetes.io/border-switch": RequiredSame,
	}}})
	if err != nil || len(constraints) != 1 || constraints["uplinkDomain"] != RequiredSame {
		t.Fatalf("combined requiredSame constraints=%v err=%v", constraints, err)
	}
}

func TestInclusterManifestReferencesExternalRawTopology(t *testing.T) {
	manifest, err := os.ReadFile("../../../deploy-incluster/algorithm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(manifest, []byte("sw.yaml: |")) || bytes.Contains(manifest, []byte("topology-metadata.yaml")) {
		t.Fatal("algorithm manifest must not inline topology data or metadata")
	}
	for _, expected := range []string{"name: ngd-ngg-algorithm-topology", "value: /etc/ngd-ngg/topology/sw.yaml"} {
		if !bytes.Contains(manifest, []byte(expected)) {
			t.Fatalf("algorithm manifest is missing %q", expected)
		}
	}

	source, err := os.ReadFile("../../../sw.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cache, err := LoadSource("", source)
	if err != nil {
		t.Fatal(err)
	}
	status := cache.Status()
	if status["leafCount"] != 234 || status["topologyMode"] != ModeUplinkCompatible || status["spineOnlyLeafCount"] != 200 || status["borderOnlyLeafCount"] != 34 {
		t.Fatalf("status=%v", status)
	}
}
