package main

import (
	"testing"
	"time"
)

func TestRefreshOptionsValidate(t *testing.T) {
	for _, test := range []struct {
		name    string
		options refreshOptions
		valid   bool
	}{
		{name: "configured", options: refreshOptions{interval: 30 * time.Second, maxConcurrent: 8}, valid: true},
		{name: "zero interval", options: refreshOptions{maxConcurrent: 5}},
		{name: "negative interval", options: refreshOptions{interval: -time.Second, maxConcurrent: 5}},
		{name: "zero workers", options: refreshOptions{interval: 15 * time.Second}},
		{name: "negative workers", options: refreshOptions{interval: 15 * time.Second, maxConcurrent: -1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.options.validate()
			if test.valid && err != nil {
				t.Fatalf("valid options were rejected: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid options were accepted")
			}
		})
	}
}

func TestRefreshDefaults(t *testing.T) {
	if defaultDemandRefreshInterval != 15*time.Second || defaultMaxConcurrentRefreshes != 5 {
		t.Fatalf("unexpected defaults: interval=%s workers=%d", defaultDemandRefreshInterval, defaultMaxConcurrentRefreshes)
	}
}
