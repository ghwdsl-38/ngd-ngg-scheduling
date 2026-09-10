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
	var interfaces, kubeconfig, kubeContext, sysClassNet, nodeName string
	var timeout, interval time.Duration
	var count int
	command := &cobra.Command{Use: "topology-agent", Short: "Collect Node-to-Leaf LLDP facts and persist them in Node metadata", RunE: func(_ *cobra.Command, _ []string) error {
		log.Printf("[LLDP-AGENT] ENTER command.RunE")
		nodeName = strings.TrimSpace(nodeName)
		if nodeName == "" {
			return fmt.Errorf("--node-name is required when NODE_NAME and hostname are empty")
		}
		if timeout <= 0 {
			return fmt.Errorf("--timeout must be greater than zero")
		}
		if interval < 0 {
			return fmt.Errorf("--interval cannot be negative")
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
				if strings.EqualFold(item, "auto") {
					continue
				}
				allowed[item] = struct{}{}
			}
		}
		log.Printf("[LLDP-AGENT] CONFIG node=%s configuredInterfaces=%v kubeconfig=%q kubeContext=%q sysClassNet=%q timeout=%s count=%d interval=%s", nodeName, sortedInterfaceSet(allowed), kubeconfig, kubeContext, sysClassNet, timeout, count, interval)
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		agent := &topologyAgent{
			client: client, nodeName: nodeName, interfaces: allowed,
			timeout: timeout, count: count, interval: interval, sysClassNet: sysClassNet,
		}
		return agent.run(ctx)
	}}
	hostname, _ := os.Hostname()
	defaultNodeName := strings.TrimSpace(os.Getenv("NODE_NAME"))
	if defaultNodeName == "" {
		defaultNodeName = hostname
	}
	defaultInterfaces := strings.TrimSpace(os.Getenv("LLDP_INTERFACES"))
	if defaultInterfaces == "" {
		defaultInterfaces = "auto"
	}
	command.Flags().StringVar(&interfaces, "interfaces", defaultInterfaces, "comma-separated capture interfaces; Bond masters are captured directly; auto discovers uplinks")
	command.Flags().StringVar(&nodeName, "node-name", defaultNodeName, "Kubernetes Node to update; defaults to NODE_NAME or hostname")
	command.Flags().StringVar(&kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"), "path to kubeconfig; defaults to in-cluster configuration")
	command.Flags().StringVar(&kubeContext, "kube-context", "", "optional context in kubeconfig")
	command.Flags().StringVar(&sysClassNet, "sys-class-net", "/sys/class/net", "sysfs network class path used for Bond discovery")
	command.Flags().DurationVar(&timeout, "timeout", 120*time.Second, "LLDP receive duration; count=0 always listens for this full window")
	command.Flags().IntVar(&count, "count", 0, "maximum unique LLDP neighbors; zero listens for the full timeout")
	command.Flags().DurationVar(&interval, "interval", 0, "repeat interval between probe cycle starts; zero runs one cycle and exits")
	return command
}
