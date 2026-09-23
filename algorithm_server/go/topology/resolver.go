package topology

import (
	"fmt"
	"sort"
	"strings"
)

// ResolveNodes joins the LLDP-observed Leaf IDs on Kubernetes Nodes to the physical graph.
func (c *Cache) ResolveNodes(nodes []map[string]any) ([]map[string]any, []string, error) {
	resolved := make([]map[string]any, 0, len(nodes))
	warnings := []string{}
	for _, original := range nodes {
		node := copyMap(original)
		nodeTopology, _ := original["topology"].(map[string]any)
		leafIDs, err := nodeLeafIDs(nodeTopology)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Node %s has invalid Leaf set: %v", stringValue(node["nodeName"]), err))
			continue
		}
		leaves := make([]resolvedLeaf, 0, len(leafIDs))
		missing := ""
		for _, id := range leafIDs {
			leaf, ok := c.byLeaf[id]
			if !ok {
				missing = id
				break
			}
			leaves = append(leaves, leaf)
		}
		if missing != "" {
			warnings = append(warnings, fmt.Sprintf("Node %s Leaf %q is absent from Algorithm topology source", stringValue(node["nodeName"]), missing))
			continue
		}
		leaf := leaves[0]
		conflict := ""
		for _, candidate := range leaves[1:] {
			if candidate.LeafDomainID != leaf.LeafDomainID {
				conflict = "NODE_LEAF_DOMAIN_CONFLICT"
				break
			}
			if c.mode == ModeUplinkCompatible && candidate.UplinkDomainID != leaf.UplinkDomainID {
				conflict = "NODE_UPLINK_DOMAIN_CONFLICT"
				break
			}
			if c.mode == ModeLayered && (candidate.SpineDomainID != leaf.SpineDomainID || candidate.BorderDomainID != leaf.BorderDomainID) {
				conflict = "NODE_UPLINK_DOMAIN_CONFLICT"
				break
			}
		}
		if conflict != "" {
			warnings = append(warnings, fmt.Sprintf("%s: Node %s Leaves %v do not share one scheduling domain", conflict, stringValue(node["nodeName"]), leafIDs))
			continue
		}
		node["topology"] = map[string]any{"topologyMode": c.mode, "regionId": leaf.RegionID, "locationId": leaf.LocationID, "dataCenterId": leaf.DataCenterID, "roomId": leaf.RoomID, "leafSwitchId": leafIDs[0], "leafSwitchIds": leafIDs, "switchId": leafIDs[0], "leafDomainId": leaf.LeafDomainID, "leafDomainSwitchIds": leaf.LeafDomainLeaves, "spineDomainId": leaf.SpineDomainID, "spineSwitchIds": leaf.SpineSwitchIDs, "borderDomainId": leaf.BorderDomainID, "borderSwitchIds": leaf.BorderSwitchIDs, "uplinkDomainId": leaf.UplinkDomainID, "uplinkKind": leaf.UplinkKind, "peerLeafSwitchIds": leaf.PeerLeafIDs, "bandwidthGbps": leaf.BandwidthGbps, "latencyMillis": leaf.LatencyMillis}
		resolved = append(resolved, node)
	}
	if len(resolved) == 0 {
		return nil, warnings, fmt.Errorf("no Node Leaf can be resolved by Algorithm topology source")
	}
	return resolved, warnings, nil
}

func nodeLeafIDs(topology map[string]any) ([]string, error) {
	values := []string{}
	switch raw := topology["leafSwitchIds"].(type) {
	case []any:
		for _, v := range raw {
			values = append(values, stringValue(v))
		}
	case []string:
		values = append(values, raw...)
	case nil:
	default:
		return nil, fmt.Errorf("leafSwitchIds must be an array")
	}
	if len(values) == 0 {
		leaf := stringValue(topology["leafSwitchId"])
		if leaf == "" {
			leaf = stringValue(topology["switchId"])
		}
		values = append(values, leaf)
	}
	set := map[string]struct{}{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	result := sortedKeys(set)
	sort.Strings(result)
	if len(result) == 0 {
		return nil, fmt.Errorf("leafSwitchIds cannot be empty")
	}
	if len(result) > 2 {
		return nil, fmt.Errorf("leafSwitchIds contains %d Leaves; maximum is 2", len(result))
	}
	return result, nil
}
