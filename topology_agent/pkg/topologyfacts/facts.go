// Package topologyfacts normalizes direct-Leaf observations and builds the
// exact Kubernetes metadata persisted by the production LLDP Agent.
package topologyfacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Prefix is the Kubernetes metadata namespace owned by the topology Agent.
const Prefix = "topology.demo.ngg.io/"

// Link is one selected host interface and its LLDP Leaf neighbor.
type Link struct {
	BondName       string `json:"bond,omitempty"`
	BondMode       string `json:"bondMode,omitempty"`
	Interface      string `json:"interface"`
	LeafSwitchID   string `json:"leafSwitchId"`
	ChassisID      string `json:"chassisId,omitempty"`
	ChassisSubtype string `json:"chassisIdSubtype,omitempty"`
	RemotePortID   string `json:"remotePortId,omitempty"`
	Active         bool   `json:"active"`
}

// Observation is the normalized direct network fact for one Node.
type Observation struct {
	LeafSwitchIDs []string `json:"leafSwitchIds"`
	Links         []Link   `json:"links"`
	Source        string   `json:"source"`
}

// Normalize validates, deduplicates and stably orders a collected result.
func Normalize(value Observation) (Observation, error) {
	linksByIdentity := map[string]Link{}
	for _, link := range value.Links {
		link.Interface = strings.TrimSpace(link.Interface)
		link.LeafSwitchID = strings.TrimSpace(link.LeafSwitchID)
		if link.Interface == "" || link.LeafSwitchID == "" {
			return Observation{}, fmt.Errorf("every Leaf link needs interface and leafSwitchId")
		}
		identity := strings.Join([]string{link.Interface, link.LeafSwitchID, strings.TrimSpace(link.RemotePortID)}, "\x00")
		linksByIdentity[identity] = link
	}
	value.Links = make([]Link, 0, len(linksByIdentity))
	leafSet := map[string]struct{}{}
	for _, link := range linksByIdentity {
		value.Links = append(value.Links, link)
		leafSet[link.LeafSwitchID] = struct{}{}
	}
	if len(leafSet) == 0 {
		return Observation{}, fmt.Errorf("no valid Leaf neighbor was collected")
	}
	sort.Slice(value.Links, func(i, j int) bool {
		if value.Links[i].Interface == value.Links[j].Interface {
			if value.Links[i].LeafSwitchID == value.Links[j].LeafSwitchID {
				return value.Links[i].RemotePortID < value.Links[j].RemotePortID
			}
			return value.Links[i].LeafSwitchID < value.Links[j].LeafSwitchID
		}
		return value.Links[i].Interface < value.Links[j].Interface
	})
	value.LeafSwitchIDs = make([]string, 0, len(leafSet))
	for leaf := range leafSet {
		value.LeafSwitchIDs = append(value.LeafSwitchIDs, leaf)
	}
	sort.Strings(value.LeafSwitchIDs)
	return value, nil
}

// BuildNodeMetadata returns the merge-patch fragments used by the Agent.
func BuildNodeMetadata(value Observation, observedAt time.Time) (Observation, map[string]any, map[string]any, error) {
	normalized, err := Normalize(value)
	if err != nil {
		return Observation{}, nil, nil, err
	}
	leavesJSON, _ := json.Marshal(normalized.LeafSwitchIDs)
	linksJSON, _ := json.Marshal(normalized.Links)
	labels := map[string]any{
		Prefix + "leaf-set-id": LeafSetID(normalized.LeafSwitchIDs),
		Prefix + "leaf-count":  strconv.Itoa(len(normalized.LeafSwitchIDs)),
		Prefix + "leaf-switch": nil,
	}
	if len(normalized.LeafSwitchIDs) == 1 && len(validation.IsValidLabelValue(normalized.LeafSwitchIDs[0])) == 0 {
		labels[Prefix+"leaf-switch"] = normalized.LeafSwitchIDs[0]
	}
	annotations := map[string]any{
		Prefix + "leaf-switch-ids": string(leavesJSON),
		Prefix + "leaf-links":      string(linksJSON),
		Prefix + "source":          normalized.Source,
		Prefix + "observed-at":     observedAt.UTC().Format(time.RFC3339),
	}
	if len(normalized.Links) > 0 {
		annotations[Prefix+"local-interface"] = normalized.Links[0].Interface
		annotations[Prefix+"remote-port"] = normalized.Links[0].RemotePortID
	}
	return normalized, labels, annotations, nil
}

// LeafSetID is a short stable identity suitable for a Kubernetes Label.
func LeafSetID(leaves []string) string {
	unique := make(map[string]struct{}, len(leaves))
	for _, leaf := range leaves {
		if leaf = strings.TrimSpace(leaf); leaf != "" {
			unique[leaf] = struct{}{}
		}
	}
	copyOfLeaves := make([]string, 0, len(unique))
	for leaf := range unique {
		copyOfLeaves = append(copyOfLeaves, leaf)
	}
	sort.Strings(copyOfLeaves)
	sum := sha256.Sum256([]byte(strings.Join(copyOfLeaves, "\x00")))
	return hex.EncodeToString(sum[:6])
}
