package topology

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type rawSource map[string]map[string]leafAdjacency

// LoadSource parses the network team's Room -> Leaf -> SPINE/BORDER/LEAF file.
func LoadSource(path string, data []byte) (*Cache, error) {
	if len(data) == 0 {
		if strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("topology source file is required")
		}
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read topology source: %w", err)
		}
	}
	var source rawSource
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&source); err != nil {
		return nil, fmt.Errorf("decode raw topology source: %w", err)
	}
	if len(source) == 0 {
		return nil, fmt.Errorf("raw topology source contains no Room")
	}
	version, err := canonicalHash(source)
	if err != nil {
		return nil, err
	}
	config, err := rawToNormalized(source, version)
	if err != nil {
		return nil, err
	}
	return buildCache(config, "raw-room-adjacency")
}

// LoadConfig keeps compatibility with the normalized test/sample topology format.
func LoadConfig(path string, data []byte) (*Cache, error) {
	if len(data) == 0 {
		if strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("topology config file is required")
		}
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read topology config: %w", err)
		}
	}
	var config normalizedConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode topology config: %w", err)
	}
	return buildCache(config, "normalized")
}

func rawToNormalized(source rawSource, version string) (normalizedConfig, error) {
	regions, locations, dataCenters, rooms := map[string]scopeConfig{}, map[string]scopeConfig{}, map[string]scopeConfig{}, map[string]scopeConfig{}
	for _, roomID := range sortedKeys(source) {
		parts := strings.Split(roomID, "-")
		if len(parts) < 3 {
			return normalizedConfig{}, fmt.Errorf("Room %q cannot derive Location and DataCenter", roomID)
		}
		regionID := parts[0]
		locationID := strings.Join(parts[:2], "-")
		dataCenterID := strings.Join(parts[:3], "-")
		regions[regionID] = scopeConfig{ID: regionID}
		locations[locationID] = scopeConfig{ID: locationID, RegionID: regionID}
		dataCenters[dataCenterID] = scopeConfig{ID: dataCenterID, LocationID: locationID}
		rooms[roomID] = scopeConfig{ID: roomID, DataCenterID: dataCenterID}
	}
	return normalizedConfig{
		Version:  version,
		Scopes:   scopesConfig{Regions: scopeValues(regions), Locations: scopeValues(locations), DataCenters: scopeValues(dataCenters), Rooms: scopeValues(rooms)},
		Topology: source,
	}, nil
}

func scopeValues(values map[string]scopeConfig) []scopeConfig {
	result := make([]scopeConfig, 0, len(values))
	for _, id := range sortedKeys(values) {
		result = append(result, values[id])
	}
	return result
}

func buildCache(config normalizedConfig, sourceFormat string) (*Cache, error) {
	if strings.TrimSpace(config.Version) == "" {
		return nil, fmt.Errorf("topology version is required")
	}
	regions, err := indexScopes("region", config.Scopes.Regions)
	if err != nil {
		return nil, err
	}
	locations, err := indexScopes("location", config.Scopes.Locations)
	if err != nil {
		return nil, err
	}
	dcs, err := indexScopes("dataCenter", config.Scopes.DataCenters)
	if err != nil {
		return nil, err
	}
	rooms, err := indexScopes("room", config.Scopes.Rooms)
	if err != nil {
		return nil, err
	}
	if len(regions) == 0 || len(locations) == 0 || len(dcs) == 0 || len(rooms) == 0 {
		return nil, fmt.Errorf("topology requires Region, Location, DataCenter and Room scopes")
	}
	for _, location := range locations {
		if _, ok := regions[location.RegionID]; !ok {
			return nil, fmt.Errorf("location %q references unknown region %q", location.ID, location.RegionID)
		}
	}
	for _, dc := range dcs {
		if _, ok := locations[dc.LocationID]; !ok {
			return nil, fmt.Errorf("dataCenter %q references unknown location %q", dc.ID, dc.LocationID)
		}
	}
	for _, room := range rooms {
		if _, ok := dcs[room.DataCenterID]; !ok {
			return nil, fmt.Errorf("room %q references unknown dataCenter %q", room.ID, room.DataCenterID)
		}
	}

	byLeaf := map[string]resolvedLeaf{}
	spineOnly, borderOnly, layered := 0, 0, 0
	for _, roomID := range sortedKeys(config.Topology) {
		room, ok := rooms[roomID]
		if !ok {
			return nil, fmt.Errorf("topology references unknown room %q", roomID)
		}
		dc := dcs[room.DataCenterID]
		location := locations[dc.LocationID]
		for _, leafID := range sortedKeys(config.Topology[roomID]) {
			if _, duplicate := byLeaf[leafID]; duplicate {
				return nil, fmt.Errorf("Leaf %q occurs in more than one room", leafID)
			}
			links := config.Topology[roomID][leafID]
			spines, borders := sortedKeys(links.Spines), sortedKeys(links.Borders)
			switch {
			case len(spines) > 0 && len(borders) > 0:
				layered++
			case len(spines) > 0:
				spineOnly++
			case len(borders) > 0:
				borderOnly++
			default:
				return nil, fmt.Errorf("Leaf %q in room %q has neither SPINE nor BORDER uplink", leafID, roomID)
			}
			metric := config.LeafMetrics[leafID]
			byLeaf[leafID] = resolvedLeaf{RegionID: location.RegionID, LocationID: location.ID, DataCenterID: dc.ID, RoomID: room.ID, LeafSwitchID: leafID, SpineSwitchIDs: spines, BorderSwitchIDs: borders, PeerLeafIDs: sortedKeys(links.Leaves), BandwidthGbps: metric.BandwidthGbps, LatencyMillis: metric.LatencyMillis}
		}
	}
	if len(byLeaf) == 0 {
		return nil, fmt.Errorf("topology contains no Leaf")
	}
	mode := ModeLayered
	if spineOnly > 0 || borderOnly > 0 {
		mode = ModeUplinkCompatible
	}

	spineDomains, borderDomains := map[string]struct{}{}, map[string]struct{}{}
	for _, leafID := range sortedKeys(byLeaf) {
		leaf := byLeaf[leafID]
		if len(leaf.SpineSwitchIDs) > 0 {
			hash, err := shortHash(leaf.SpineSwitchIDs)
			if err != nil {
				return nil, err
			}
			leaf.SpineDomainID = "spine-domain:" + hash
			spineDomains[leaf.SpineDomainID] = struct{}{}
		}
		if len(leaf.BorderSwitchIDs) > 0 {
			leaf.BorderDomainID = configuredBorderDomain(config.BorderDomains, leaf.RoomID, leaf.BorderSwitchIDs)
			if leaf.BorderDomainID == "" {
				hash, err := shortHash(leaf.BorderSwitchIDs)
				if err != nil {
					return nil, err
				}
				leaf.BorderDomainID = "border-domain:" + hash
			}
			borderDomains[leaf.BorderDomainID] = struct{}{}
		}
		if mode == ModeUplinkCompatible {
			switch {
			case leaf.SpineDomainID != "" && leaf.BorderDomainID != "":
				hash, err := shortHash([]string{leaf.SpineDomainID, leaf.BorderDomainID})
				if err != nil {
					return nil, err
				}
				leaf.UplinkKind = "layered"
				leaf.UplinkDomainID = "layered:" + hash
			case leaf.SpineDomainID != "":
				leaf.UplinkKind = "spine"
				leaf.UplinkDomainID = "spine:" + leaf.SpineDomainID
			default:
				leaf.UplinkKind = "border"
				leaf.UplinkDomainID = "border:" + leaf.BorderDomainID
			}
		}
		byLeaf[leafID] = leaf
	}
	if err := assignLeafDomains(byLeaf, mode); err != nil {
		return nil, err
	}

	cache := &Cache{version: config.Version, mode: mode, sourceFormat: sourceFormat, byLeaf: byLeaf, dataCenters: stringSet(sortedKeys(dcs)), rooms: stringSet(sortedKeys(rooms)), borderAliases: map[string]string{}, spineAliases: map[string]string{}, leafAliases: map[string]string{}, roomCount: len(rooms), spineDomainCount: len(spineDomains), borderDomainCount: len(borderDomains), spineOnlyLeafCount: spineOnly, borderOnlyLeafCount: borderOnly, layeredLeafCount: layered}
	leafDomains := map[string]struct{}{}
	for _, leaf := range byLeaf {
		leafDomains[leaf.LeafDomainID] = struct{}{}
		if err := addAlias(cache.leafAliases, leaf.LeafDomainID, leaf.LeafDomainID, "Leaf"); err != nil {
			return nil, err
		}
		for _, id := range leaf.LeafDomainLeaves {
			if err := addAlias(cache.leafAliases, id, leaf.LeafDomainID, "Leaf"); err != nil {
				return nil, err
			}
		}
		if leaf.SpineDomainID != "" {
			if err := addAlias(cache.spineAliases, leaf.SpineDomainID, leaf.SpineDomainID, "Spine"); err != nil {
				return nil, err
			}
			for _, id := range leaf.SpineSwitchIDs {
				if err := addAlias(cache.spineAliases, id, leaf.SpineDomainID, "Spine"); err != nil {
					return nil, err
				}
			}
		}
		if leaf.BorderDomainID != "" {
			if err := addAlias(cache.borderAliases, leaf.BorderDomainID, leaf.BorderDomainID, "Border"); err != nil {
				return nil, err
			}
			for _, id := range leaf.BorderSwitchIDs {
				if err := addAlias(cache.borderAliases, id, leaf.BorderDomainID, "Border"); err != nil {
					return nil, err
				}
			}
		}
	}
	cache.leafDomainCount = len(leafDomains)
	identity := map[string]any{"version": config.Version, "mode": mode, "topology": config.Topology}
	cache.snapshotID, err = canonicalHash(identity)
	if err != nil {
		return nil, err
	}
	return cache, nil
}

func configuredBorderDomain(domains map[string]borderDomainConfig, roomID string, members []string) string {
	ids := []string{}
	for id, d := range domains {
		if d.RoomID == roomID && equalSet(d.Members, members) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) == 1 {
		return ids[0]
	}
	return ""
}

func indexScopes(kind string, values []scopeConfig) (map[string]scopeConfig, error) {
	result := map[string]scopeConfig{}
	for _, v := range values {
		if v.ID == "" {
			return nil, fmt.Errorf("%s id is required", kind)
		}
		if _, ok := result[v.ID]; ok {
			return nil, fmt.Errorf("duplicate %s %q", kind, v.ID)
		}
		result[v.ID] = v
	}
	return result, nil
}

func addAlias(aliases map[string]string, alias, domain, kind string) error {
	if existing, ok := aliases[alias]; ok && existing != domain {
		return fmt.Errorf("%s switch %q maps to multiple logical domains", kind, alias)
	}
	aliases[alias] = domain
	return nil
}

func assignLeafDomains(byLeaf map[string]resolvedLeaf, mode string) error {
	processed := map[string]struct{}{}
	for _, leafID := range sortedKeys(byLeaf) {
		if _, done := processed[leafID]; done {
			continue
		}
		leaf := byLeaf[leafID]
		members := uniqueSorted(append([]string{leafID}, leaf.PeerLeafIDs...))
		if len(members) > 2 {
			return fmt.Errorf("Leaf %q resolves to %d-member Leaf domain; maximum is 2", leafID, len(members))
		}
		for _, memberID := range members {
			member, ok := byLeaf[memberID]
			if !ok {
				return fmt.Errorf("Leaf %q references unknown peer Leaf %q", leafID, memberID)
			}
			expected := []string{}
			for _, id := range members {
				if id != memberID {
					expected = append(expected, id)
				}
			}
			if !equalSet(member.PeerLeafIDs, expected) {
				return fmt.Errorf("LEAF_PEER_ASYMMETRIC: Leaf %q peers=%v expected=%v", memberID, member.PeerLeafIDs, expected)
			}
			if member.RoomID != leaf.RoomID {
				return fmt.Errorf("Leaf domain members %q and %q must share Room", leafID, memberID)
			}
			if mode == ModeUplinkCompatible && (member.UplinkDomainID != leaf.UplinkDomainID || member.UplinkKind != leaf.UplinkKind) {
				return fmt.Errorf("LEAF_UPLINK_TYPE_CONFLICT: Leaf domain members %q and %q use different uplinks", leafID, memberID)
			}
			if mode == ModeLayered && (member.SpineDomainID != leaf.SpineDomainID || member.BorderDomainID != leaf.BorderDomainID) {
				return fmt.Errorf("Leaf domain members %q and %q must share Spine and Border domains", leafID, memberID)
			}
		}
		domainID := leafID
		if len(members) > 1 {
			hash, err := shortHash(members)
			if err != nil {
				return err
			}
			domainID = "pair-" + hash
		}
		for _, id := range members {
			member := byLeaf[id]
			member.LeafDomainID = domainID
			member.LeafDomainLeaves = append([]string(nil), members...)
			byLeaf[id] = member
			processed[id] = struct{}{}
		}
	}
	return nil
}
