package topology

const (
	ModeLayered          = "layered"
	ModeUplinkCompatible = "uplink-compatible"
	RequiredSame         = "requiredSame"
)

type linkConfig struct {
	LocalPort string `yaml:"local_port,omitempty" json:"local_port,omitempty"`
	PeerPort  string `yaml:"peer_port,omitempty" json:"peer_port,omitempty"`
}

type leafAdjacency struct {
	Spines  map[string]linkConfig `yaml:"SPINE" json:"SPINE"`
	Borders map[string]linkConfig `yaml:"BORDER" json:"BORDER"`
	Leaves  map[string]linkConfig `yaml:"LEAF" json:"LEAF"`
}

type scopeConfig struct {
	ID           string `yaml:"id" json:"id"`
	Name         string `yaml:"name,omitempty" json:"name,omitempty"`
	RegionID     string `yaml:"regionId,omitempty" json:"regionId,omitempty"`
	LocationID   string `yaml:"locationId,omitempty" json:"locationId,omitempty"`
	DataCenterID string `yaml:"dataCenterId,omitempty" json:"dataCenterId,omitempty"`
}

type scopesConfig struct {
	Regions     []scopeConfig `yaml:"regions" json:"regions"`
	Locations   []scopeConfig `yaml:"locations" json:"locations"`
	DataCenters []scopeConfig `yaml:"dataCenters" json:"dataCenters"`
	Rooms       []scopeConfig `yaml:"rooms" json:"rooms"`
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

type normalizedConfig struct {
	Version       string                              `yaml:"version" json:"version"`
	Scopes        scopesConfig                        `yaml:"scopes" json:"scopes"`
	BorderDomains map[string]borderDomainConfig       `yaml:"borderDomains" json:"borderDomains"`
	LeafMetrics   map[string]leafMetricConfig         `yaml:"leafMetrics,omitempty" json:"leafMetrics,omitempty"`
	Topology      map[string]map[string]leafAdjacency `yaml:"topology" json:"topology"`
}

type resolvedLeaf struct {
	RegionID, LocationID, DataCenterID, RoomID string
	LeafSwitchID, LeafDomainID                 string
	LeafDomainLeaves                           []string
	SpineDomainID, BorderDomainID              string
	SpineSwitchIDs, BorderSwitchIDs            []string
	UplinkDomainID, UplinkKind                 string
	PeerLeafIDs                                []string
	BandwidthGbps, LatencyMillis               float64
}

type Cache struct {
	snapshotID, version, mode, sourceFormat  string
	byLeaf                                   map[string]resolvedLeaf
	dataCenters, rooms                       map[string]struct{}
	borderAliases, spineAliases, leafAliases map[string]string
	roomCount, leafDomainCount               int
	spineDomainCount, borderDomainCount      int
	spineOnlyLeafCount, borderOnlyLeafCount  int
	layeredLeafCount                         int
}

func (c *Cache) SnapshotID() string {
	if c == nil {
		return ""
	}
	return c.snapshotID
}
func (c *Cache) Version() string {
	if c == nil {
		return ""
	}
	return c.version
}
func (c *Cache) Mode() string {
	if c == nil {
		return ""
	}
	return c.mode
}
