package topology

import (
	"fmt"
	"strings"
)

var demandKeys = map[string]string{
	"topology.kubernetes.io/data-center":   "dataCenter",
	"topology.kubernetes.io/room":          "room",
	"topology.kubernetes.io/border-switch": "borderDomain",
	"topology.kubernetes.io/spine-switch":  "spineDomain",
	"topology.kubernetes.io/leaf-switch":   "leafDomain",
}

// ResolveDemandTopologyLabels converts the stable NGD labels into internal constraints.
func (c *Cache) ResolveDemandTopologyLabels(request map[string]any) (map[string]any, []string, error) {
	ngd, ok := request["ngd"].(map[string]any)
	if !ok {
		return nil, nil, nil
	}
	raw, exists := ngd["topologyLabels"]
	if !exists || raw == nil {
		return nil, nil, nil
	}
	labels, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("spec.topologyLabels must be an object")
	}
	requested := map[string]string{}
	for key, rawValue := range labels {
		level, supported := demandKeys[key]
		if !supported {
			return nil, nil, fmt.Errorf("spec.topologyLabels key %q is unsupported; supported levels are data-center, room, border-switch, spine-switch and leaf-switch", key)
		}
		value, ok := rawValue.(string)
		value = strings.TrimSpace(value)
		if !ok || value == "" {
			return nil, nil, fmt.Errorf("spec.topologyLabels[%q] must be a non-empty string", key)
		}
		requested[level] = value
	}

	constraints := map[string]any{}
	if err := resolveScope(constraints, requested, "dataCenter", c.dataCenters); err != nil {
		return nil, nil, err
	}
	if err := resolveScope(constraints, requested, "room", c.rooms); err != nil {
		return nil, nil, err
	}
	if err := resolveDomain(constraints, requested, "leafDomain", c.leafAliases); err != nil {
		return nil, nil, err
	}

	if c.mode == ModeUplinkCompatible {
		if err := c.resolveCompatibleUplink(constraints, requested); err != nil {
			return nil, nil, err
		}
	} else {
		if err := resolveDomain(constraints, requested, "spineDomain", c.spineAliases); err != nil {
			return nil, nil, err
		}
		if err := resolveDomain(constraints, requested, "borderDomain", c.borderAliases); err != nil {
			return nil, nil, err
		}
	}
	if !c.hasMatchingLeaf(constraints) {
		return nil, nil, fmt.Errorf("TOPOLOGY_CONSTRAINT_CONFLICT: spec.topologyLabels selects no Leaf in the configured topology")
	}
	return constraints, nil, nil
}

func (c *Cache) resolveCompatibleUplink(constraints map[string]any, requested map[string]string) error {
	spine, hasSpine := requested["spineDomain"]
	border, hasBorder := requested["borderDomain"]
	if hasSpine {
		if spine == RequiredSame {
			constraints["uplinkDomain"] = RequiredSame
		} else {
			domain, ok := c.spineAliases[spine]
			if !ok {
				return fmt.Errorf("TOPOLOGY_SWITCH_NOT_FOUND: Spine switch or domain %q is absent from the configured topology", spine)
			}
			constraints["spineDomain"] = domain
			constraints["uplinkDomain"] = RequiredSame
		}
	}
	if hasBorder {
		if border == RequiredSame {
			constraints["uplinkDomain"] = RequiredSame
		} else {
			domain, ok := c.borderAliases[border]
			if !ok {
				return fmt.Errorf("TOPOLOGY_SWITCH_NOT_FOUND: Border switch or domain %q is absent from the configured topology", border)
			}
			constraints["borderDomain"] = domain
			constraints["uplinkDomain"] = RequiredSame
		}
	}
	return nil
}

func resolveScope(constraints map[string]any, requested map[string]string, level string, known map[string]struct{}) error {
	value, exists := requested[level]
	if !exists {
		return nil
	}
	if value != RequiredSame {
		if _, ok := known[value]; !ok {
			return fmt.Errorf("spec.topologyLabels %s %q is absent from the configured topology", level, value)
		}
	}
	constraints[level] = value
	return nil
}

func resolveDomain(constraints map[string]any, requested map[string]string, level string, aliases map[string]string) error {
	value, exists := requested[level]
	if !exists {
		return nil
	}
	if value == RequiredSame {
		constraints[level] = value
		return nil
	}
	domain, ok := aliases[value]
	if !ok {
		return fmt.Errorf("TOPOLOGY_SWITCH_NOT_FOUND: %s switch or domain %q is absent from the configured topology", level, value)
	}
	constraints[level] = domain
	return nil
}

func (c *Cache) hasMatchingLeaf(constraints map[string]any) bool {
	for _, leaf := range c.byLeaf {
		if leafMatches(leaf, constraints) {
			return true
		}
	}
	return false
}

func leafMatches(leaf resolvedLeaf, constraints map[string]any) bool {
	values := map[string]string{"dataCenter": leaf.DataCenterID, "room": leaf.RoomID, "leafDomain": leaf.LeafDomainID, "spineDomain": leaf.SpineDomainID, "borderDomain": leaf.BorderDomainID, "uplinkDomain": leaf.UplinkDomainID}
	for level, raw := range constraints {
		actual, known := values[level]
		if !known {
			continue
		}
		expected := stringValue(raw)
		if expected == RequiredSame {
			if actual == "" {
				return false
			}
			continue
		}
		if actual != expected {
			return false
		}
	}
	return true
}
