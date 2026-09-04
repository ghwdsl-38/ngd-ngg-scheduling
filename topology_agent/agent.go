package main

import (
	"context"
	"encoding/json"
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
	resync      time.Duration
	sysClassNet string
}

// run deliberately probes Linux Bond/LLDP state on its own cadence. A Node
// watch cannot reveal a network failover that has not yet changed Node data.
func (a *topologyAgent) run(ctx context.Context) error {
	if a.resync <= 0 {
		a.resync = 30 * time.Second
	}
	if a.sysClassNet == "" {
		a.sysClassNet = "/sys/class/net"
	}
	for ctx.Err() == nil {
		node, err := a.client.getNode(ctx, a.nodeName)
		if err != nil {
			log.Printf("get Node: %v", err)
		} else if err := a.reconcile(ctx, node); err != nil {
			// Keep the last persisted topology when a collection window fails.
			log.Printf("reconcile Node %s: %v", a.nodeName, err)
		}
		if !wait(ctx, a.resync) {
			break
		}
	}
	return ctx.Err()
}

func (a *topologyAgent) reconcile(ctx context.Context, node nodeObject) error {
	observed, err := a.collect()
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
		return nil
	}

	if err := a.client.patchNode(ctx, a.nodeName, labels, annotations); err != nil {
		return err
	}
	log.Printf("node=%s source=%s leafSet=%s leaves=%v", a.nodeName, observed.Source, setID, observed.LeafSwitchIDs)
	return nil
}

func (a *topologyAgent) collect() (observation, error) {
	selections, err := selectLLDPInterfaces(a.sysClassNet, a.interfaces)
	if err != nil {
		return observation{}, err
	}
	allowed := map[string]struct{}{}
	for name := range selections {
		allowed[name] = struct{}{}
	}
	neighbors, err := receiveLLDP(allowed, a.listen)
	if err != nil {
		return observation{}, err
	}
	links := make([]leafLink, 0, len(neighbors))
	for _, neighbor := range neighbors {
		selection := selections[neighbor.LocalInterface]
		mode := selection.BondMode
		if mode == "" {
			mode = "direct"
		}
		links = append(links, leafLink{
			BondName: selection.BondName, BondMode: mode, Interface: neighbor.LocalInterface,
			LeafSwitchID: neighbor.switchID(), RemotePortID: neighbor.PortID, Active: selection.Active || len(selections) == 0,
		})
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
