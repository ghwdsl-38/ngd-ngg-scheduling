package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func main() {
	if err := newCommand().Execute(); err != nil {
		os.Exit(1)
	}
}

func newCommand() *cobra.Command {
	var mode, interfaces string
	var listenSeconds float64
	command := &cobra.Command{Use: "topology-agent", Short: "Collect Node-to-Leaf LLDP and persist a three-level topology in Node labels", RunE: func(_ *cobra.Command, _ []string) error {
		nodeName := os.Getenv("NODE_NAME")
		if nodeName == "" {
			return fmt.Errorf("NODE_NAME is required")
		}
		client, err := inClusterClient()
		if err != nil {
			return err
		}
		allowed := map[string]struct{}{}
		for _, item := range strings.Split(interfaces, ",") {
			if item = strings.TrimSpace(item); item != "" {
				allowed[item] = struct{}{}
			}
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		agent := &topologyAgent{client: client, nodeName: nodeName, mode: mode, interfaces: allowed, listen: time.Duration(listenSeconds * float64(time.Second))}
		return agent.run(ctx)
	}}
	command.Flags().StringVar(&mode, "mode", valueOr(os.Getenv("COLLECTION_MODE"), "Simulated"), "Simulated or LLDP")
	command.Flags().StringVar(&interfaces, "interfaces", os.Getenv("LLDP_INTERFACES"), "comma-separated LLDP interfaces")
	command.Flags().Float64Var(&listenSeconds, "listen-seconds", 35, "real LLDP listen timeout")
	return command
}
