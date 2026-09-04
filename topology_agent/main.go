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
	var interfaces, kubeconfig, kubeContext, sysClassNet string
	var listenSeconds, resyncSeconds float64
	command := &cobra.Command{Use: "topology-agent", Short: "Collect Node-to-Leaf LLDP facts and persist them in Node metadata", RunE: func(_ *cobra.Command, _ []string) error {
		nodeName := os.Getenv("NODE_NAME")
		if nodeName == "" {
			return fmt.Errorf("NODE_NAME is required")
		}
		config, err := loadKubernetesConfig(kubeconfig, kubeContext)
		if err != nil {
			return err
		}
		client, err := newKubeClient(config)
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
		agent := &topologyAgent{
			client: client, nodeName: nodeName, interfaces: allowed,
			listen: time.Duration(listenSeconds * float64(time.Second)),
			resync: time.Duration(resyncSeconds * float64(time.Second)), sysClassNet: sysClassNet,
		}
		return agent.run(ctx)
	}}
	command.Flags().StringVar(&interfaces, "interfaces", os.Getenv("LLDP_INTERFACES"), "comma-separated LLDP interfaces")
	command.Flags().StringVar(&kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"), "path to kubeconfig; defaults to in-cluster configuration")
	command.Flags().StringVar(&kubeContext, "kube-context", "", "optional context in kubeconfig")
	command.Flags().StringVar(&sysClassNet, "sys-class-net", "/sys/class/net", "sysfs network class path used for Bond discovery")
	command.Flags().Float64Var(&listenSeconds, "listen-seconds", 35, "real LLDP listen timeout")
	command.Flags().Float64Var(&resyncSeconds, "resync-seconds", 30, "seconds between Bond and LLDP topology probes")
	return command
}
