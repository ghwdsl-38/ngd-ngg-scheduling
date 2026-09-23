package algorithm

import (
	"fmt"
	"sort"
	"strings"
)

const prometheusNodeIPLabel = "paas_node_ip"

func bindMetricSnapshotToNodes(snapshot *metricSnapshot, static staticSnapshot, identityLabel string) (*metricSnapshot, bool, []string) {
	if snapshot == nil || identityLabel != prometheusNodeIPLabel {
		return snapshot, false, nil
	}

	nodeByIP := make(map[string]string, len(static.Nodes))
	invalidIPs := map[string]struct{}{}
	nodesWithoutIP := []string{}
	for _, node := range static.Nodes {
		name := stringValue(node["nodeName"])
		ip := strings.TrimSpace(stringValue(node["nodeIP"]))
		if ip == "" {
			nodesWithoutIP = append(nodesWithoutIP, name)
			continue
		}
		if previous, exists := nodeByIP[ip]; exists && previous != name {
			delete(nodeByIP, ip)
			invalidIPs[ip] = struct{}{}
			continue
		}
		if _, invalid := invalidIPs[ip]; !invalid {
			nodeByIP[ip] = name
		}
	}

	identities := make([]string, 0, len(snapshot.Nodes))
	for identity := range snapshot.Nodes {
		identities = append(identities, identity)
	}
	sort.Strings(identities)

	mapped := make(map[string]map[string]float64, len(snapshot.Nodes))
	unknownIdentities := []string{}
	ambiguousNodes := map[string]struct{}{}
	for _, identity := range identities {
		name, found := nodeByIP[strings.TrimSpace(identity)]
		if !found {
			unknownIdentities = append(unknownIdentities, identity)
			continue
		}
		if _, duplicate := mapped[name]; duplicate {
			delete(mapped, name)
			ambiguousNodes[name] = struct{}{}
			continue
		}
		if _, ambiguous := ambiguousNodes[name]; !ambiguous {
			mapped[name] = snapshot.Nodes[identity]
		}
	}

	missingNodes := []string{}
	for _, node := range static.Nodes {
		name := stringValue(node["nodeName"])
		if _, found := mapped[name]; !found {
			missingNodes = append(missingNodes, name)
		}
	}

	warnings := []string{}
	if len(invalidIPs) > 0 {
		warnings = append(warnings, fmt.Sprintf("Prometheus identity mapping found %d InternalIP values assigned to multiple Nodes: %s", len(invalidIPs), previewSet(invalidIPs)))
	}
	if len(nodesWithoutIP) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d static Nodes have no Kubernetes InternalIP: %s", len(nodesWithoutIP), previewStrings(nodesWithoutIP)))
	}
	if len(unknownIdentities) > 0 {
		warnings = append(warnings, fmt.Sprintf("ignored %d Prometheus paas_node_ip identities absent from the static snapshot: %s", len(unknownIdentities), previewStrings(unknownIdentities)))
	}
	if len(ambiguousNodes) > 0 {
		warnings = append(warnings, fmt.Sprintf("multiple Prometheus IP identities resolved to %d Nodes; their metrics were ignored: %s", len(ambiguousNodes), previewSet(ambiguousNodes)))
	}
	if len(missingNodes) > 0 {
		warnings = append(warnings, fmt.Sprintf("Prometheus metrics are missing for %d static Nodes: %s", len(missingNodes), previewStrings(missingNodes)))
	}

	copy := *snapshot
	copy.Nodes = mapped
	return &copy, len(warnings) > 0, warnings
}

func previewSet(values map[string]struct{}) string {
	items := make([]string, 0, len(values))
	for value := range values {
		items = append(items, value)
	}
	return previewStrings(items)
}

func previewStrings(values []string) string {
	items := append([]string(nil), values...)
	sort.Strings(items)
	const limit = 10
	if len(items) > limit {
		return strings.Join(items[:limit], ", ") + fmt.Sprintf(" ... (+%d)", len(items)-limit)
	}
	return strings.Join(items, ", ")
}
