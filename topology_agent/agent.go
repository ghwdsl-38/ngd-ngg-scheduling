package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

const labelPrefix = "topology.demo.ngg.io/"

type observation struct {
	LeafSwitchID    string
	BorderSwitchID  string
	CoreSwitchID    string
	BandwidthGbps   float64
	LatencyMillis   float64
	LocalInterface  string
	RemotePortID    string
	Source          string
	TopologyVersion string
}

type topologyAgent struct {
	client     *kubeClient
	nodeName   string
	mode       string
	interfaces map[string]struct{}
	listen     time.Duration
	config     topologyConfig
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
		leaf := node.Metadata.Labels[labelPrefix+"switch"]
		if leaf == "" {
			return fmt.Errorf("simulated seed label %sswitch is missing", labelPrefix)
		}
		observed, err = a.config.resolve(leaf)
		observed.LocalInterface = valueOr(node.Metadata.Labels[labelPrefix+"local-interface"], "eth0")
		observed.RemotePortID = node.Metadata.Annotations[labelPrefix+"remote-port"]
		observed.Source = "SimulatedLLDP"
	} else {
		var neighbor lldpNeighbor
		neighbor, err = receiveLLDP(a.interfaces, a.listen)
		if err == nil {
			observed, err = a.config.resolve(neighbor.switchID())
			observed.LocalInterface, observed.RemotePortID, observed.Source = neighbor.LocalInterface, neighbor.PortID, "LLDP"
		}
	}
	if err != nil {
		return err
	}
	labels := map[string]string{
		labelPrefix + "switch": observed.LeafSwitchID, labelPrefix + "leaf-switch": observed.LeafSwitchID,
		labelPrefix + "border-switch": observed.BorderSwitchID, labelPrefix + "core-switch": observed.CoreSwitchID,
		labelPrefix + "bandwidth-gbps": formatFloat(observed.BandwidthGbps), labelPrefix + "latency-ms": formatFloat(observed.LatencyMillis),
		labelPrefix + "topology-version": observed.TopologyVersion, labelPrefix + "source": observed.Source,
	}
	annotations := map[string]string{labelPrefix + "local-interface": observed.LocalInterface, labelPrefix + "remote-port": observed.RemotePortID, labelPrefix + "observed-at": time.Now().UTC().Format(time.RFC3339)}
	if err := a.client.patchNode(ctx, a.nodeName, labels, annotations); err != nil {
		return err
	}
	a.mu.Lock()
	a.current = &observed
	a.mu.Unlock()
	log.Printf("node=%s source=%s leaf=%s border=%s core=%s", a.nodeName, observed.Source, observed.LeafSwitchID, observed.BorderSwitchID, observed.CoreSwitchID)
	return nil
}

func observationFromLabels(labels, annotations map[string]string) (observation, bool) {
	result := observation{LeafSwitchID: labels[labelPrefix+"leaf-switch"], BorderSwitchID: labels[labelPrefix+"border-switch"], CoreSwitchID: labels[labelPrefix+"core-switch"], Source: labels[labelPrefix+"source"], TopologyVersion: labels[labelPrefix+"topology-version"], LocalInterface: annotations[labelPrefix+"local-interface"], RemotePortID: annotations[labelPrefix+"remote-port"]}
	result.BandwidthGbps, _ = strconv.ParseFloat(labels[labelPrefix+"bandwidth-gbps"], 64)
	result.LatencyMillis, _ = strconv.ParseFloat(labels[labelPrefix+"latency-ms"], 64)
	return result, result.LeafSwitchID != "" && result.BorderSwitchID != "" && result.CoreSwitchID != "" && result.TopologyVersion != ""
}

func formatFloat(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }
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
