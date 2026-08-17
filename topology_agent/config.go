package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type topologyConfig struct {
	Version        string                  `json:"version"`
	LeafSwitches   map[string]leafConfig   `json:"leafSwitches"`
	BorderSwitches map[string]borderConfig `json:"borderSwitches"`
}

type leafConfig struct {
	BorderSwitchID string  `json:"borderSwitchId"`
	BandwidthGbps  float64 `json:"bandwidthGbps"`
	LatencyMillis  float64 `json:"latencyMillis"`
}

type borderConfig struct {
	CoreSwitchID string `json:"coreSwitchId"`
}

func loadTopologyConfig(path string) (topologyConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return topologyConfig{}, fmt.Errorf("read topology config: %w", err)
	}
	var config topologyConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return topologyConfig{}, fmt.Errorf("decode topology config: %w", err)
	}
	if config.Version == "" || len(config.LeafSwitches) == 0 || len(config.BorderSwitches) == 0 {
		return topologyConfig{}, fmt.Errorf("topology config requires version, leafSwitches and borderSwitches")
	}
	for leaf, item := range config.LeafSwitches {
		border, found := config.BorderSwitches[item.BorderSwitchID]
		if leaf == "" || item.BorderSwitchID == "" || !found || border.CoreSwitchID == "" {
			return topologyConfig{}, fmt.Errorf("leaf %q references an invalid Border/Core path", leaf)
		}
	}
	return config, nil
}

func (c topologyConfig) resolve(leaf string) (observation, error) {
	leafItem, found := c.LeafSwitches[leaf]
	if !found {
		return observation{}, fmt.Errorf("leaf switch %q is not present in static topology config", leaf)
	}
	border := c.BorderSwitches[leafItem.BorderSwitchID]
	return observation{LeafSwitchID: leaf, BorderSwitchID: leafItem.BorderSwitchID, CoreSwitchID: border.CoreSwitchID, BandwidthGbps: leafItem.BandwidthGbps, LatencyMillis: leafItem.LatencyMillis, TopologyVersion: c.Version}, nil
}
