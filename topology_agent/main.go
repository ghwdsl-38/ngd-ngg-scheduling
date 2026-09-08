package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Printf("[LLDP-AGENT] ENTER main")
	if err := newCommand().Execute(); err != nil {
		log.Printf("[LLDP-AGENT] FAILED: %v", err)
		os.Exit(1)
	}
	log.Printf("[LLDP-AGENT] EXIT main status=success")
}

func newCommand() *cobra.Command {
	var interfaces, kubeconfig, kubeContext, sysClassNet string
	var listenSeconds, idleSeconds, resyncSeconds float64
	var count int
	command := &cobra.Command{Use: "topology-agent", Short: "Collect Node-to-Leaf LLDP facts and persist them in Node metadata", RunE: func(_ *cobra.Command, _ []string) error {
		log.Printf("[LLDP-AGENT] ENTER command.RunE")
		nodeName := os.Getenv("NODE_NAME")
		if nodeName == "" {
			return fmt.Errorf("NODE_NAME is required")
		}
		if listenSeconds <= 0 {
			return fmt.Errorf("--listen-seconds must be greater than zero")
		}
		if idleSeconds < 0 {
			return fmt.Errorf("--idle-seconds cannot be negative")
		}
		if resyncSeconds <= 0 {
			return fmt.Errorf("--resync-seconds must be greater than zero")
		}
		if count < 0 {
			return fmt.Errorf("--count cannot be negative")
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
		log.Printf("[LLDP-AGENT] CONFIG node=%s configuredInterfaces=%v kubeconfig=%q kubeContext=%q sysClassNet=%q listen=%gs idle=%gs count=%d resync=%gs", nodeName, sortedInterfaceSet(allowed), kubeconfig, kubeContext, sysClassNet, listenSeconds, idleSeconds, count, resyncSeconds)
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		agent := &topologyAgent{
			client: client, nodeName: nodeName, interfaces: allowed,
			listen: time.Duration(listenSeconds * float64(time.Second)),
			idle:   time.Duration(idleSeconds * float64(time.Second)),
			count:  count,
			resync: time.Duration(resyncSeconds * float64(time.Second)), sysClassNet: sysClassNet,
		}
		return agent.run(ctx)
	}}
	command.Flags().StringVar(&interfaces, "interfaces", os.Getenv("LLDP_INTERFACES"), "comma-separated LLDP interfaces")
	command.Flags().StringVar(&kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"), "path to kubeconfig; defaults to in-cluster configuration")
	command.Flags().StringVar(&kubeContext, "kube-context", "", "optional context in kubeconfig")
	command.Flags().StringVar(&sysClassNet, "sys-class-net", "/sys/class/net", "sysfs network class path used for Bond discovery")
	command.Flags().Float64Var(&listenSeconds, "listen-seconds", 120, "real LLDP listen timeout")
	command.Flags().Float64Var(&idleSeconds, "idle-seconds", 3, "finish a scan after no new unique neighbor appears")
	command.Flags().IntVar(&count, "count", 0, "maximum unique LLDP neighbors; zero uses idle/max timeout")
	command.Flags().Float64Var(&resyncSeconds, "resync-seconds", 30, "seconds between Bond and LLDP topology probes")
	return command
}
