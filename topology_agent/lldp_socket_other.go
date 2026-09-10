//go:build !linux

package main

import (
	"context"
	"fmt"
	"time"
)

func receiveLLDPOnHost(context.Context, map[string]interfaceSelection, bool, time.Duration, int) ([]lldpNeighbor, error) {
	return nil, fmt.Errorf("LLDP raw socket capture requires Linux")
}
