package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const labelPrefix = "topology.demo.ngg.io/"

type observation struct {
	LeafSwitchID   string
	LocalInterface string
	RemotePortID   string
	Source         string
}

type topologyAgent struct {
	client     *kubeClient
	nodeName   string
	mode       string
	interfaces map[string]struct{}
	listen     time.Duration
	mu         sync.RWMutex
	current    *observation
}

func (a *topologyAgent) run(ctx context.Context) error {
	for ctx.Err() == nil {
		node, err := a.client.getNode(ctx, a.nodeName)
		if err != nil {
			log.Printf("get Node: %v", err)
			if !wait(ctx, 5*time.Second) {
				break
			}
			continue
		}
		if err := a.reconcile(ctx, node); err != nil {
			log.Printf("reconcile Node %s: %v", a.nodeName, err)
		}
		err = a.client.watchNode(ctx, a.nodeName, node.Metadata.ResourceVersion, func(updated nodeObject) error { return a.reconcile(ctx, updated) })
		if err != nil && ctx.Err() == nil {
			log.Printf("watch Node %s: %v", a.nodeName, err)
		}
	}
	return ctx.Err()
}

func (a *topologyAgent) reconcile(ctx context.Context, node nodeObject) error {
	if restored, ok := observationFromLabels(node.Metadata.Labels, node.Metadata.Annotations); ok {
		a.mu.Lock()
		a.current = &restored
		a.mu.Unlock()
		return nil
	}
	var observed observation
	var err error
	if strings.EqualFold(a.mode, "Simulated") {
		leaf := node.Metadata.Annotations[labelPrefix+"simulated-leaf-switch"]
		if leaf == "" {
			return fmt.Errorf("simulated seed annotation %ssimulated-leaf-switch is missing", labelPrefix)
		}
		observed.LeafSwitchID = leaf
		observed.LocalInterface = valueOr(node.Metadata.Labels[labelPrefix+"local-interface"], "eth0")
		observed.RemotePortID = node.Metadata.Annotations[labelPrefix+"remote-port"]
		observed.Source = "SimulatedLLDP"
	} else {
		var neighbor lldpNeighbor
		neighbor, err = receiveLLDP(a.interfaces, a.listen)
		if err == nil {
			observed.LeafSwitchID = neighbor.switchID()
			observed.LocalInterface, observed.RemotePortID, observed.Source = neighbor.LocalInterface, neighbor.PortID, "LLDP"
		}
	}
	if err != nil {
		return err
	}
	labels := map[string]string{
		labelPrefix + "leaf-switch": observed.LeafSwitchID,
	}
	annotations := map[string]string{labelPrefix + "source": observed.Source, labelPrefix + "local-interface": observed.LocalInterface, labelPrefix + "remote-port": observed.RemotePortID, labelPrefix + "observed-at": time.Now().UTC().Format(time.RFC3339)}
	if err := a.client.patchNode(ctx, a.nodeName, labels, annotations); err != nil {
		return err
	}
	a.mu.Lock()
	a.current = &observed
	a.mu.Unlock()
	log.Printf("node=%s source=%s leaf=%s", a.nodeName, observed.Source, observed.LeafSwitchID)
	return nil
}

func observationFromLabels(labels, annotations map[string]string) (observation, bool) {
	result := observation{LeafSwitchID: labels[labelPrefix+"leaf-switch"], Source: annotations[labelPrefix+"source"], LocalInterface: annotations[labelPrefix+"local-interface"], RemotePortID: annotations[labelPrefix+"remote-port"]}
	return result, result.LeafSwitchID != ""
}
func valueOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
func wait(ctx context.Context, duration time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(duration):
		return true
	}
}
