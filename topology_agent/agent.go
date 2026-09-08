package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"demo.ngg/topology-agent/pkg/topologyfacts"
)

const labelPrefix = topologyfacts.Prefix

type leafLink = topologyfacts.Link
type observation = topologyfacts.Observation

type topologyAgent struct {
	client      *kubeClient
	nodeName    string
	interfaces  map[string]struct{}
	listen      time.Duration
	idle        time.Duration
	count       int
	resync      time.Duration
	sysClassNet string
}

// run deliberately probes Linux Bond/LLDP state on its own cadence. A Node
// watch cannot reveal a network failover that has not yet changed Node data.
func (a *topologyAgent) run(ctx context.Context) error {
	log.Printf("[LLDP-AGENT] ENTER topologyAgent.run node=%s", a.nodeName)
	if a.resync <= 0 {
		a.resync = 3 * time.Minute
	}
	if a.sysClassNet == "" {
		a.sysClassNet = "/sys/class/net"
	}
	for cycle := 1; ctx.Err() == nil; cycle++ {
		cycleStarted := time.Now()
		log.Printf("[LLDP-AGENT] CYCLE start number=%d", cycle)
		log.Printf("[LLDP-AGENT] CALL Kubernetes Nodes.Get node=%s", a.nodeName)
		node, err := a.client.getNode(ctx, a.nodeName)
		if err != nil {
			log.Printf("[LLDP-AGENT] ERROR Kubernetes Nodes.Get node=%s: %v", a.nodeName, err)
		} else if err := a.reconcile(ctx, node); err != nil {
			// Keep the last persisted topology when a collection window fails.
			log.Printf("[LLDP-AGENT] ERROR reconcile Node %s: %v", a.nodeName, err)
		}
		nextCycleWait := a.resync - time.Since(cycleStarted)
		if nextCycleWait < 0 {
			nextCycleWait = 0
		}
		log.Printf("[LLDP-AGENT] WAIT nextCycle=%s cadence=%s", nextCycleWait, a.resync)
		if !wait(ctx, nextCycleWait) {
			break
		}
	}
	return ctx.Err()
}

func (a *topologyAgent) reconcile(ctx context.Context, node nodeObject) error {
	started := time.Now()
	log.Printf("[LLDP-AGENT] ENTER topologyAgent.reconcile node=%s", a.nodeName)
	defer func() { log.Printf("[LLDP-AGENT] EXIT topologyAgent.reconcile elapsed=%s", time.Since(started)) }()
	observed, err := a.collect(ctx)
	if err != nil {
		return err
	}
	observed, labels, annotations, err := topologyfacts.BuildNodeMetadata(observed, time.Now())
	if err != nil {
		return err
	}
	leavesJSON, _ := json.Marshal(observed.LeafSwitchIDs)
	linksJSON, _ := json.Marshal(observed.Links)
	setID := topologyfacts.LeafSetID(observed.LeafSwitchIDs)
	if node.Metadata.Labels[labelPrefix+"leaf-set-id"] == setID &&
		node.Metadata.Annotations[labelPrefix+"leaf-switch-ids"] == string(leavesJSON) &&
		node.Metadata.Annotations[labelPrefix+"leaf-links"] == string(linksJSON) &&
		node.Metadata.Annotations[labelPrefix+"source"] == observed.Source {
		log.Printf("[LLDP-AGENT] SKIP Kubernetes Nodes.Patch node=%s reason=topology-unchanged leafSet=%s", a.nodeName, setID)
		return nil
	}

	log.Printf("[LLDP-AGENT] CALL Kubernetes Nodes.Patch node=%s leafSet=%s leaves=%v", a.nodeName, setID, observed.LeafSwitchIDs)
	if err := a.client.patchNode(ctx, a.nodeName, labels, annotations); err != nil {
		return err
	}
	log.Printf("[LLDP-AGENT] RESULT node=%s source=%s leafSet=%s leaves=%v", a.nodeName, observed.Source, setID, observed.LeafSwitchIDs)
	return nil
}

func (a *topologyAgent) collect(ctx context.Context) (observation, error) {
	log.Printf("[LLDP-AGENT] ENTER topologyAgent.collect")
	selections, err := selectLLDPInterfaces(a.sysClassNet, a.interfaces)
	if err != nil {
		return observation{}, err
	}
	for _, selection := range sortedSelections(selections) {
		log.Printf("[LLDP-AGENT] SELECT interface=%s index=%d kind=%s adminUp=%t carrier=%q operState=%q bond=%q mode=%q active=%t mii=%q", selection.Name, selection.Index, selection.Kind, selection.AdministrativeUp, selection.Carrier, selection.OperState, selection.BondName, selection.BondMode, selection.Active, selection.MIIStatus)
	}
	neighbors, err := receiveLLDP(ctx, selections, len(a.interfaces) == 0, a.listen, a.idle, a.count)
	if err != nil {
		return observation{}, err
	}
	links, err := buildLeafLinks(neighbors, selections)
	if err != nil {
		return observation{}, err
	}
	return observation{Links: links, Source: "LLDP"}, nil
}

// buildLeafLinks converts lldp-new-2-compatible neighbor observations into
// the direct-Leaf facts owned by this Agent. Chassis ID is the physical Leaf
// identity; System Name remains the ID shared with Algorithm topology config.
func buildLeafLinks(neighbors []lldpNeighbor, selections map[string]interfaceSelection) ([]leafLink, error) {
	type resolvedLeaf struct {
		id             string
		fromSystemName bool
	}
	leafByChassis := map[string]resolvedLeaf{}
	for _, neighbor := range neighbors {
		if neighbor.LooksLikeLocalHost {
			log.Printf("[LLDP-AGENT] REJECT neighbor systemName=%q chassisID=%q reason=looks-like-local-host", neighbor.SystemName, neighbor.ChassisID)
			continue
		}
		_, exists := selections[neighbor.LocalInterface]
		if !exists {
			return nil, fmt.Errorf("LLDP neighbor arrived on unselected interface %q", neighbor.LocalInterface)
		}
		chassisIdentity := leafChassisIdentity(neighbor)
		if chassisIdentity == "" {
			return nil, fmt.Errorf("LLDP neighbor on %s has no usable chassis identity", neighbor.LocalInterface)
		}
		resolved := leafByChassis[chassisIdentity]
		systemName := strings.TrimSpace(neighbor.SystemName)
		if systemName != "" {
			if resolved.fromSystemName && !strings.EqualFold(resolved.id, systemName) {
				return nil, fmt.Errorf("one LLDP chassis %q advertised conflicting Leaf names %q and %q", neighbor.ChassisID, resolved.id, systemName)
			}
			resolved = resolvedLeaf{id: systemName, fromSystemName: true}
		} else if resolved.id == "" {
			resolved.id = strings.TrimSpace(neighbor.ChassisID)
		}
		if resolved.id == "" {
			return nil, fmt.Errorf("LLDP neighbor on %s has no usable Leaf ID", neighbor.LocalInterface)
		}
		leafByChassis[chassisIdentity] = resolved
	}
	if len(leafByChassis) == 0 {
		return nil, fmt.Errorf("no external upper-switch LLDP neighbor found")
	}
	if len(leafByChassis) > 2 {
		return nil, fmt.Errorf("Node resolved %d distinct LLDP chassis; current design supports at most 2 Leaf switches", len(leafByChassis))
	}
	chassisByLeafName := map[string]string{}
	for chassisIdentity, leaf := range leafByChassis {
		leafNameKey := strings.ToLower(leaf.id)
		if previous, found := chassisByLeafName[leafNameKey]; found && previous != chassisIdentity {
			return nil, fmt.Errorf("Leaf name %q was advertised by two different LLDP chassis; refusing an ambiguous topology", leaf.id)
		}
		chassisByLeafName[leafNameKey] = chassisIdentity
	}

	links := make([]leafLink, 0, len(neighbors))
	for _, neighbor := range neighbors {
		if neighbor.LooksLikeLocalHost {
			continue
		}
		selection := selections[neighbor.LocalInterface]
		chassisIdentity := leafChassisIdentity(neighbor)
		leafID := leafByChassis[chassisIdentity].id
		mode := selection.BondMode
		if mode == "" {
			mode = "direct"
		}
		links = append(links, leafLink{
			BondName: selection.BondName, BondMode: mode, Interface: neighbor.LocalInterface,
			LeafSwitchID: leafID, ChassisID: neighbor.ChassisID, ChassisSubtype: neighbor.ChassisIDSubtype,
			RemotePortID: neighbor.PortID, Active: selection.Active,
		})
	}
	return links, nil
}

func wait(ctx context.Context, duration time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(duration):
		return true
	}
}
