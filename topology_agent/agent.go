package main

import (
	"context"
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
	timeout     time.Duration
	count       int
	interval    time.Duration
	sysClassNet string
}

// run executes one probe by default. A positive interval enables periodic
// probing because a Node watch cannot reveal a network failover that has not
// yet changed Node data.
func (a *topologyAgent) run(ctx context.Context) error {
	log.Printf("[LLDP-AGENT] ENTER topologyAgent.run node=%s", a.nodeName)
	if a.sysClassNet == "" {
		a.sysClassNet = "/sys/class/net"
	}
	if a.interval <= 0 {
		log.Printf("[LLDP-AGENT] RUN mode=one-shot")
		if err := a.reconcile(ctx); err != nil {
			return fmt.Errorf("reconcile Node %s: %w", a.nodeName, err)
		}
		log.Printf("[LLDP-AGENT] ONE-SHOT complete node=%s", a.nodeName)
		return nil
	}
	log.Printf("[LLDP-AGENT] RUN mode=periodic interval=%s", a.interval)
	for cycle := 1; ctx.Err() == nil; cycle++ {
		cycleStarted := time.Now()
		log.Printf("[LLDP-AGENT] CYCLE start number=%d", cycle)
		if err := a.reconcile(ctx); err != nil {
			// Keep the last persisted topology when a collection window fails.
			log.Printf("[LLDP-AGENT] ERROR reconcile Node %s: %v", a.nodeName, err)
		}
		nextCycleWait := a.interval - time.Since(cycleStarted)
		if nextCycleWait < 0 {
			nextCycleWait = 0
		}
		log.Printf("[LLDP-AGENT] WAIT nextCycle=%s cadence=%s", nextCycleWait, a.interval)
		if !wait(ctx, nextCycleWait) {
			break
		}
	}
	return ctx.Err()
}

func (a *topologyAgent) reconcile(ctx context.Context) error {
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
	log.Printf("[LLDP-AGENT] METADATA BUILT node=%s leafCount=%d leafSet=%s linkCount=%d", a.nodeName, len(observed.LeafSwitchIDs), topologyfacts.LeafSetID(observed.LeafSwitchIDs), len(observed.Links))
	setID := topologyfacts.LeafSetID(observed.LeafSwitchIDs)
	log.Printf("[LLDP-AGENT] KUBERNETES METADATA SYNC START node=%s leafSet=%s leaves=%v", a.nodeName, setID, observed.LeafSwitchIDs)
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
	bindSingle := shouldBindSingleExplicitInterface(a.interfaces, selections)
	neighbors, err := receiveLLDP(ctx, selections, bindSingle, a.timeout, a.count)
	if err != nil {
		return observation{}, err
	}
	links, err := buildLeafLinks(neighbors, selections)
	if err != nil {
		return observation{}, err
	}
	log.Printf("[LLDP-AGENT] LEAF LINKS BUILT mode=%s links=%d", collectionScope(len(a.interfaces) == 0), len(links))
	return observation{Links: links, Source: "LLDP"}, nil
}

// Bind a single explicitly selected interface, including a Bond master.
func shouldBindSingleExplicitInterface(configured map[string]struct{}, selected map[string]interfaceSelection) bool {
	if len(configured) != 1 || len(selected) != 1 {
		return false
	}
	for name := range configured {
		_, exists := selected[name]
		return exists
	}
	return false
}

func collectionScope(automatic bool) string {
	if automatic {
		return "automatic"
	}
	return "explicit"
}

// buildLeafLinks converts lldp-new-3-compatible neighbor observations into
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
