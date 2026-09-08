package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
		a.resync = 30 * time.Second
	}
	if a.sysClassNet == "" {
		a.sysClassNet = "/sys/class/net"
	}
	for cycle := 1; ctx.Err() == nil; cycle++ {
		log.Printf("[LLDP-AGENT] CYCLE start number=%d", cycle)
		log.Printf("[LLDP-AGENT] CALL Kubernetes Nodes.Get node=%s", a.nodeName)
		node, err := a.client.getNode(ctx, a.nodeName)
		if err != nil {
			log.Printf("[LLDP-AGENT] ERROR Kubernetes Nodes.Get node=%s: %v", a.nodeName, err)
		} else if err := a.reconcile(ctx, node); err != nil {
			// Keep the last persisted topology when a collection window fails.
			log.Printf("[LLDP-AGENT] ERROR reconcile Node %s: %v", a.nodeName, err)
		}
		log.Printf("[LLDP-AGENT] WAIT resync=%s", a.resync)
		if !wait(ctx, a.resync) {
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
	links := make([]leafLink, 0, len(neighbors))
	for _, neighbor := range neighbors {
		if neighbor.LooksLikeLocalHost {
			log.Printf("[LLDP-AGENT] REJECT neighbor systemName=%q chassisID=%q reason=looks-like-local-host", neighbor.SystemName, neighbor.ChassisID)
			continue
		}
		selection := selections[neighbor.LocalInterface]
		mode := selection.BondMode
		if mode == "" {
			mode = "direct"
		}
		links = append(links, leafLink{
			BondName: selection.BondName, BondMode: mode, Interface: neighbor.LocalInterface,
			LeafSwitchID: neighbor.switchID(), RemotePortID: neighbor.PortID, Active: selection.Active,
		})
	}
	if len(links) == 0 {
		return observation{}, fmt.Errorf("no external upper-switch LLDP neighbor found")
	}
	return observation{Links: links, Source: "LLDP"}, nil
}

func wait(ctx context.Context, duration time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(duration):
		return true
	}
}
