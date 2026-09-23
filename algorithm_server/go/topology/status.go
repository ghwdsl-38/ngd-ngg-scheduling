package topology

func (c *Cache) Status() map[string]any {
	if c == nil {
		return map[string]any{"ready": false, "snapshotId": "", "version": "", "topologyMode": "", "sourceFormat": "", "leafCount": 0, "roomCount": 0, "leafDomainCount": 0, "spineDomainCount": 0, "borderDomainCount": 0, "spineOnlyLeafCount": 0, "borderOnlyLeafCount": 0, "layeredLeafCount": 0}
	}
	return map[string]any{"ready": len(c.byLeaf) > 0, "snapshotId": c.snapshotID, "version": c.version, "topologyMode": c.mode, "sourceFormat": c.sourceFormat, "leafCount": len(c.byLeaf), "roomCount": c.roomCount, "leafDomainCount": c.leafDomainCount, "spineDomainCount": c.spineDomainCount, "borderDomainCount": c.borderDomainCount, "spineOnlyLeafCount": c.spineOnlyLeafCount, "borderOnlyLeafCount": c.borderOnlyLeafCount, "layeredLeafCount": c.layeredLeafCount}
}
