// topology.go loads the operator-maintained network graph and resolves the
// one or two Leaves observed by LLDP into a single stable scheduling domain.
// Kubernetes Nodes carry only direct-Leaf facts; Region/Location/DC/Room and
// all switch relationships stay inside Algorithm Server.
package algorithm

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type topologyScopeConfig struct {
	ID           string `yaml:"id" json:"id"`
	Name         string `yaml:"name,omitempty" json:"name,omitempty"`
	RegionID     string `yaml:"regionId,omitempty" json:"regionId,omitempty"`
	LocationID   string `yaml:"locationId,omitempty" json:"locationId,omitempty"`
	DataCenterID string `yaml:"dataCenterId,omitempty" json:"dataCenterId,omitempty"`
}

type topologyScopesConfig struct {
	Regions     []topologyScopeConfig `yaml:"regions" json:"regions"`
	Locations   []topologyScopeConfig `yaml:"locations" json:"locations"`
	DataCenters []topologyScopeConfig `yaml:"dataCenters" json:"dataCenters"`
	Rooms       []topologyScopeConfig `yaml:"rooms" json:"rooms"`
}

type topologyLinkConfig struct {
	LocalPort string `yaml:"local_port,omitempty" json:"local_port,omitempty"`
	PeerPort  string `yaml:"peer_port,omitempty" json:"peer_port,omitempty"`
}

// The upper-case field names intentionally match China Unicom's topology
// document. Empty SPINE maps are valid and mean that Leaf connects directly
// to its Border domain in this room.
type leafAdjacencyConfig struct {
	Spines  map[string]topologyLinkConfig `yaml:"SPINE" json:"SPINE"`
	Borders map[string]topologyLinkConfig `yaml:"BORDER" json:"BORDER"`
	Leaves  map[string]topologyLinkConfig `yaml:"LEAF" json:"LEAF"`
}

type borderDomainConfig struct {
	RoomID  string   `yaml:"roomId" json:"roomId"`
	Mode    string   `yaml:"mode,omitempty" json:"mode,omitempty"`
	Members []string `yaml:"members" json:"members"`
}

type leafMetricConfig struct {
	BandwidthGbps float64 `yaml:"bandwidthGbps,omitempty" json:"bandwidthGbps,omitempty"`
	LatencyMillis float64 `yaml:"latencyMillis,omitempty" json:"latencyMillis,omitempty"`
}

type networkTopologyConfig struct {
	Version       string                                    `yaml:"version" json:"version"`
	Scopes        topologyScopesConfig                      `yaml:"scopes" json:"scopes"`
	BorderDomains map[string]borderDomainConfig             `yaml:"borderDomains" json:"borderDomains"`
	LeafMetrics   map[string]leafMetricConfig               `yaml:"leafMetrics,omitempty" json:"leafMetrics,omitempty"`
	Topology      map[string]map[string]leafAdjacencyConfig `yaml:"topology" json:"topology"`
}

type resolvedLeafTopology struct {
	RegionID         string
	LocationID       string
	DataCenterID     string
	RoomID           string
	LeafSwitchID     string
	LeafDomainID     string
	LeafDomainLeaves []string
	SpineDomainID    string
	SpineSwitchIDs   []string
	BorderDomainID   string
	BorderSwitchIDs  []string
	PeerLeafIDs      []string
	BandwidthGbps    float64
	LatencyMillis    float64
}

type topologyCache struct {
	snapshotID        string
	version           string
	byLeaf            map[string]resolvedLeafTopology
	dataCenters       map[string]struct{}
	rooms             map[string]struct{}
	borderAliases     map[string]string
	spineAliases      map[string]string
	leafAliases       map[string]string
	roomCount         int
	borderDomainCount int
}

const requiredSame = "requiredSame"

var demandTopologyKeys = map[string]string{
	"topology.kubernetes.io/data-center":   "dataCenter",
	"topology.kubernetes.io/room":          "room",
	"topology.kubernetes.io/border-switch": "borderDomain",
	"topology.kubernetes.io/spine-switch":  "spineDomain",
	"topology.kubernetes.io/leaf-switch":   "leafDomain",
}

func loadTopologyCache(path string, data []byte) (*topologyCache, error) {
	var err error
	if len(data) == 0 {
		if path == "" {
			return nil, fmt.Errorf("topology config file is required")
		}
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read topology config: %w", err)
		}
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var config networkTopologyConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode topology config: %w", err)
	}
	return normalizeTopologyConfig(config)
}

func normalizeTopologyConfig(config networkTopologyConfig) (*topologyCache, error) {
	if strings.TrimSpace(config.Version) == "" {
		return nil, fmt.Errorf("topology config version is required")
	}
	regions, err := indexScopes("region", config.Scopes.Regions)
	if err != nil {
		return nil, err
	}
	locations, err := indexScopes("location", config.Scopes.Locations)
	if err != nil {
		return nil, err
	}
	dataCenters, err := indexScopes("dataCenter", config.Scopes.DataCenters)
	if err != nil {
		return nil, err
	}
	rooms, err := indexScopes("room", config.Scopes.Rooms)
	if err != nil {
		return nil, err
	}
	if len(regions) == 0 || len(locations) == 0 || len(dataCenters) == 0 || len(rooms) == 0 {
		return nil, fmt.Errorf("topology config requires Region, Location, DataCenter and Room scopes")
	}
	for _, location := range locations {
		if _, ok := regions[location.RegionID]; !ok {
			return nil, fmt.Errorf("location %q references unknown region %q", location.ID, location.RegionID)
		}
	}
	for _, dc := range dataCenters {
		if _, ok := locations[dc.LocationID]; !ok {
			return nil, fmt.Errorf("dataCenter %q references unknown location %q", dc.ID, dc.LocationID)
		}
	}
	for _, room := range rooms {
		if _, ok := dataCenters[room.DataCenterID]; !ok {
			return nil, fmt.Errorf("room %q references unknown dataCenter %q", room.ID, room.DataCenterID)
		}
	}

	byLeaf := map[string]resolvedLeafTopology{}
	knownBorders := map[string]struct{}{}
	for roomID, leaves := range config.Topology {
		room, ok := rooms[roomID]
		if !ok {
			return nil, fmt.Errorf("topology references unknown room %q", roomID)
		}
		dc := dataCenters[room.DataCenterID]
		location := locations[dc.LocationID]
		for leafID, links := range leaves {
			if leafID == "" {
				return nil, fmt.Errorf("room %q contains empty Leaf id", roomID)
			}
			if _, duplicate := byLeaf[leafID]; duplicate {
				return nil, fmt.Errorf("Leaf %q occurs in more than one room", leafID)
			}
			borders := sortedKeys(links.Borders)
			if len(borders) == 0 {
				return nil, fmt.Errorf("Leaf %q has no Border link", leafID)
			}
			for _, id := range borders {
				knownBorders[id] = struct{}{}
			}
			spines := sortedKeys(links.Spines)
			metric := config.LeafMetrics[leafID]
			byLeaf[leafID] = resolvedLeafTopology{
				RegionID: location.RegionID, LocationID: location.ID, DataCenterID: dc.ID, RoomID: room.ID,
				LeafSwitchID: leafID, SpineSwitchIDs: spines, BorderSwitchIDs: borders,
				PeerLeafIDs: sortedKeys(links.Leaves), BandwidthGbps: metric.BandwidthGbps, LatencyMillis: metric.LatencyMillis,
			}
		}
	}
	if len(byLeaf) == 0 {
		return nil, fmt.Errorf("topology config contains no Leaf")
	}

	for domainID, domain := range config.BorderDomains {
		if domainID == "" || domain.RoomID == "" || len(domain.Members) == 0 {
			return nil, fmt.Errorf("Border domain needs id, roomId and members")
		}
		if _, ok := rooms[domain.RoomID]; !ok {
			return nil, fmt.Errorf("Border domain %q references unknown room %q", domainID, domain.RoomID)
		}
		for _, member := range domain.Members {
			if _, ok := knownBorders[member]; !ok {
				return nil, fmt.Errorf("Border domain %q references unknown Border %q", domainID, member)
			}
		}
	}
	for leafID, leaf := range byLeaf {
		domainID := ""
		for candidateID, domain := range config.BorderDomains {
			if domain.RoomID == leaf.RoomID && equalStringsAsSet(domain.Members, leaf.BorderSwitchIDs) {
				if domainID != "" {
					return nil, fmt.Errorf("Leaf %q matches multiple Border domains", leafID)
				}
				domainID = candidateID
			}
		}
		if domainID == "" {
			return nil, fmt.Errorf("Leaf %q Border set has no explicit Border domain", leafID)
		}
		leaf.BorderDomainID = domainID
		if len(leaf.SpineSwitchIDs) > 0 {
			id, _ := canonicalHash(leaf.SpineSwitchIDs)
			leaf.SpineDomainID = "spine-domain:" + id[len("sha256:"):len("sha256:")+12]
		}
		for _, peer := range leaf.PeerLeafIDs {
			if _, ok := byLeaf[peer]; !ok {
				return nil, fmt.Errorf("Leaf %q references unknown peer Leaf %q", leafID, peer)
			}
		}
		byLeaf[leafID] = leaf
	}
	if err := assignLeafDomains(byLeaf); err != nil {
		return nil, err
	}
	borderAliases := map[string]string{}
	spineAliases := map[string]string{}
	leafAliases := map[string]string{}
	for _, leaf := range byLeaf {
		if err := addTopologyAlias(borderAliases, leaf.BorderDomainID, leaf.BorderDomainID, "Border"); err != nil {
			return nil, err
		}
		for _, switchID := range leaf.BorderSwitchIDs {
			if err := addTopologyAlias(borderAliases, switchID, leaf.BorderDomainID, "Border"); err != nil {
				return nil, err
			}
		}
		if leaf.SpineDomainID != "" {
			if err := addTopologyAlias(spineAliases, leaf.SpineDomainID, leaf.SpineDomainID, "Spine"); err != nil {
				return nil, err
			}
			for _, switchID := range leaf.SpineSwitchIDs {
				if err := addTopologyAlias(spineAliases, switchID, leaf.SpineDomainID, "Spine"); err != nil {
					return nil, err
				}
			}
		}
		if err := addTopologyAlias(leafAliases, leaf.LeafDomainID, leaf.LeafDomainID, "Leaf"); err != nil {
			return nil, err
		}
		for _, switchID := range leaf.LeafDomainLeaves {
			if err := addTopologyAlias(leafAliases, switchID, leaf.LeafDomainID, "Leaf"); err != nil {
				return nil, err
			}
		}
	}

	identity := map[string]any{"version": config.Version, "scopes": config.Scopes, "borderDomains": config.BorderDomains, "leafMetrics": config.LeafMetrics, "topology": config.Topology}
	id, err := canonicalHash(identity)
	if err != nil {
		return nil, err
	}
	return &topologyCache{
		snapshotID: id, version: config.Version, byLeaf: byLeaf,
		dataCenters: stringSet(sortedKeys(dataCenters)), rooms: stringSet(sortedKeys(rooms)),
		borderAliases: borderAliases, spineAliases: spineAliases, leafAliases: leafAliases,
		roomCount: len(rooms), borderDomainCount: len(config.BorderDomains),
	}, nil
}

func addTopologyAlias(aliases map[string]string, alias, domain, kind string) error {
	if alias == "" || domain == "" {
		return fmt.Errorf("%s topology alias and domain must not be empty", kind)
	}
	if existing, found := aliases[alias]; found && existing != domain {
		return fmt.Errorf("%s switch %q maps to multiple logical domains: %q and %q", kind, alias, existing, domain)
	}
	aliases[alias] = domain
	return nil
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

// resolveDemandTopologyLabels validates the China Unicom topologyLabels map
// and converts physical Border/Spine/Leaf names into stable logical-domain
// IDs. The original NGD remains unchanged; topologyConstraints is an internal
// Go-to-Python field for this calculation only.
func (c *topologyCache) resolveDemandTopologyLabels(request map[string]any) (map[string]any, []string, error) {
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
	requested := make(map[string]string, len(labels))
	for key, rawValue := range labels {
		level, supported := demandTopologyKeys[key]
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
	if err := resolveScopeConstraint(constraints, requested, "dataCenter", c.dataCenters); err != nil {
		return nil, nil, err
	}
	if err := resolveScopeConstraint(constraints, requested, "room", c.rooms); err != nil {
		return nil, nil, err
	}
	if err := resolveDomainConstraint(constraints, requested, "borderDomain", c.borderAliases); err != nil {
		return nil, nil, err
	}
	if err := resolveDomainConstraint(constraints, requested, "leafDomain", c.leafAliases); err != nil {
		return nil, nil, err
	}
	if !c.hasMatchingLeaf(constraints, false) {
		return nil, nil, fmt.Errorf("spec.topologyLabels selects no Leaf in the configured topology")
	}

	warnings := []string{}
	if value, requestedSpine := requested["spineDomain"]; requestedSpine {
		if value == requiredSame {
			if c.hasSpineInMatchingScope(constraints) {
				constraints["spineDomain"] = requiredSame
			} else {
				fallback := fallbackToBorderSame(constraints)
				warnings = append(warnings, "SPINE_EMPTY_FALLBACK: requested same Spine but the matching topology scope has no Spine; "+fallback)
			}
		} else if domain, found := c.spineAliases[value]; found {
			constraints["spineDomain"] = domain
			if !c.hasMatchingLeaf(constraints, true) {
				return nil, nil, fmt.Errorf("spec.topologyLabels Spine %q conflicts with the other topology constraints", value)
			}
		} else {
			fallback := fallbackToBorderSame(constraints)
			warnings = append(warnings, fmt.Sprintf("SPINE_NOT_FOUND_FALLBACK: Spine %q is absent from the configured topology; %s", value, fallback))
		}
	}
	return constraints, warnings, nil
}

func resolveScopeConstraint(constraints map[string]any, requested map[string]string, level string, known map[string]struct{}) error {
	value, exists := requested[level]
	if !exists {
		return nil
	}
	if value != requiredSame {
		if _, found := known[value]; !found {
			return fmt.Errorf("spec.topologyLabels %s %q is absent from the configured topology", level, value)
		}
	}
	constraints[level] = value
	return nil
}

func resolveDomainConstraint(constraints map[string]any, requested map[string]string, level string, aliases map[string]string) error {
	value, exists := requested[level]
	if !exists {
		return nil
	}
	if value == requiredSame {
		constraints[level] = value
		return nil
	}
	domain, found := aliases[value]
	if !found {
		return fmt.Errorf("spec.topologyLabels %s switch or domain %q is absent from the configured topology", level, value)
	}
	constraints[level] = domain
	return nil
}

func fallbackToBorderSame(constraints map[string]any) string {
	if existing, alreadyConstrained := constraints["borderDomain"]; alreadyConstrained {
		delete(constraints, "spineDomain")
		return fmt.Sprintf("using existing border-switch constraint %q", stringValue(existing))
	}
	constraints["borderDomain"] = requiredSame
	delete(constraints, "spineDomain")
	return "using border-switch=requiredSame"
}

func (c *topologyCache) hasSpineInMatchingScope(constraints map[string]any) bool {
	for _, leaf := range c.byLeaf {
		if topologyLeafMatches(leaf, constraints, false) && leaf.SpineDomainID != "" {
			return true
		}
	}
	return false
}

func (c *topologyCache) hasMatchingLeaf(constraints map[string]any, includeSpine bool) bool {
	for _, leaf := range c.byLeaf {
		if topologyLeafMatches(leaf, constraints, includeSpine) {
			return true
		}
	}
	return false
}

func topologyLeafMatches(leaf resolvedLeafTopology, constraints map[string]any, includeSpine bool) bool {
	values := map[string]string{
		"dataCenter": leaf.DataCenterID, "room": leaf.RoomID,
		"borderDomain": leaf.BorderDomainID, "leafDomain": leaf.LeafDomainID,
	}
	if includeSpine {
		values["spineDomain"] = leaf.SpineDomainID
	}
	for level, actual := range values {
		expected, exists := constraints[level]
		if !exists || expected == requiredSame {
			continue
		}
		if stringValue(expected) != actual {
			return false
		}
	}
	return true
}

// assignLeafDomains turns a configured pair of peer Leaves into one stable,
// non-overlapping scheduling domain. Single Leaves remain singleton domains.
func assignLeafDomains(byLeaf map[string]resolvedLeafTopology) error {
	processed := map[string]struct{}{}
	ids := make([]string, 0, len(byLeaf))
	for id := range byLeaf {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, leafID := range ids {
		if _, done := processed[leafID]; done {
			continue
		}
		leaf := byLeaf[leafID]
		members := append([]string{leafID}, leaf.PeerLeafIDs...)
		members = uniqueSortedStrings(members)
		if len(members) > 2 {
			return fmt.Errorf("Leaf %q resolves to %d-member Leaf domain; maximum is 2", leafID, len(members))
		}
		for _, memberID := range members {
			member, ok := byLeaf[memberID]
			if !ok {
				return fmt.Errorf("Leaf %q references unknown peer Leaf %q", leafID, memberID)
			}
			expectedPeers := make([]string, 0, len(members)-1)
			for _, candidate := range members {
				if candidate != memberID {
					expectedPeers = append(expectedPeers, candidate)
				}
			}
			if !equalStringsAsSet(member.PeerLeafIDs, expectedPeers) {
				return fmt.Errorf("Leaf domain relationship is not symmetric for %q: peers=%v expected=%v", memberID, member.PeerLeafIDs, expectedPeers)
			}
			if member.RoomID != leaf.RoomID || member.BorderDomainID != leaf.BorderDomainID {
				return fmt.Errorf("Leaf domain members %q and %q must share Room and Border domain", leafID, memberID)
			}
		}
		domainID := leafID
		if len(members) > 1 {
			hash, err := canonicalHash(members)
			if err != nil {
				return err
			}
			domainID = "pair-" + hash[len("sha256:"):len("sha256:")+12]
		}
		for _, memberID := range members {
			member := byLeaf[memberID]
			member.LeafDomainID = domainID
			member.LeafDomainLeaves = append([]string(nil), members...)
			byLeaf[memberID] = member
			processed[memberID] = struct{}{}
		}
	}
	return nil
}

func uniqueSortedStrings(values []string) []string {
	set := map[string]struct{}{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func indexScopes(kind string, values []topologyScopeConfig) (map[string]topologyScopeConfig, error) {
	result := make(map[string]topologyScopeConfig, len(values))
	for _, value := range values {
		if value.ID == "" {
			return nil, fmt.Errorf("%s id is required", kind)
		}
		if _, duplicate := result[value.ID]; duplicate {
			return nil, fmt.Errorf("duplicate %s %q", kind, value.ID)
		}
		result[value.ID] = value
	}
	return result, nil
}

func sortedKeys[T any](values map[string]T) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func equalStringsAsSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	a, b := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (c *topologyCache) resolve(snapshot staticSnapshot) (staticSnapshot, []string, error) {
	resolved := snapshot
	resolved.TopologyVersion = c.version
	resolved.Nodes = make([]map[string]any, 0, len(snapshot.Nodes))
	warnings := []string{}
	for _, original := range snapshot.Nodes {
		node := copyMap(original)
		topology, _ := original["topology"].(map[string]any)
		leafIDs, leafIDsErr := staticNodeLeafIDs(topology)
		if leafIDsErr != nil {
			warnings = append(warnings, fmt.Sprintf("Node %s has invalid Leaf set: %v", stringValue(node["nodeName"]), leafIDsErr))
			continue
		}
		resolvedLeaves := make([]resolvedLeafTopology, 0, len(leafIDs))
		missing := ""
		for _, leafID := range leafIDs {
			leaf, ok := c.byLeaf[leafID]
			if !ok {
				missing = leafID
				break
			}
			resolvedLeaves = append(resolvedLeaves, leaf)
		}
		if missing != "" {
			warnings = append(warnings, fmt.Sprintf("Node %s Leaf %q is absent from Algorithm topology config", stringValue(node["nodeName"]), missing))
			continue
		}
		leaf := resolvedLeaves[0]
		conflict := false
		for _, candidate := range resolvedLeaves[1:] {
			if candidate.LeafDomainID != leaf.LeafDomainID {
				conflict = true
				break
			}
		}
		if conflict {
			warnings = append(warnings, fmt.Sprintf("NODE_LEAF_DOMAIN_CONFLICT: Node %s Leaves %v do not share one Leaf domain", stringValue(node["nodeName"]), leafIDs))
			continue
		}
		resolvedTopology := map[string]any{
			"regionId": leaf.RegionID, "locationId": leaf.LocationID, "dataCenterId": leaf.DataCenterID, "roomId": leaf.RoomID,
			"leafSwitchId": leafIDs[0], "leafSwitchIds": leafIDs, "switchId": leafIDs[0],
			"leafDomainId": leaf.LeafDomainID, "leafDomainSwitchIds": leaf.LeafDomainLeaves,
			"borderDomainId": leaf.BorderDomainID, "borderSwitchIds": leaf.BorderSwitchIDs,
			"spineDomainId": leaf.SpineDomainID, "spineSwitchIds": leaf.SpineSwitchIDs,
			"peerLeafSwitchIds": leaf.PeerLeafIDs, "bandwidthGbps": leaf.BandwidthGbps, "latencyMillis": leaf.LatencyMillis,
		}
		node["topology"] = resolvedTopology
		resolved.Nodes = append(resolved.Nodes, node)
	}
	if len(resolved.Nodes) == 0 {
		return staticSnapshot{}, warnings, fmt.Errorf("no Node Leaf can be resolved by Algorithm topology config")
	}
	return resolved, warnings, nil
}

func (c *topologyCache) status() map[string]any {
	if c == nil {
		return map[string]any{"ready": false, "snapshotId": "", "version": "", "leafCount": 0, "roomCount": 0, "borderDomainCount": 0}
	}
	return map[string]any{"ready": len(c.byLeaf) > 0, "snapshotId": c.snapshotID, "version": c.version, "leafCount": len(c.byLeaf), "roomCount": c.roomCount, "borderDomainCount": c.borderDomainCount}
}
