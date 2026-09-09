package main

import (
	"fmt"
	"log"
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// loadKubernetesConfig supports an explicit kubeconfig while preserving the
// existing in-cluster default. An explicitly selected file never silently
// falls back to another identity or cluster.
func loadKubernetesConfig(kubeconfigPath, kubeContext string) (*rest.Config, error) {
	log.Printf("[LLDP-AGENT] KUBERNETES CONFIG START explicitPath=%t path=%q context=%q", kubeconfigPath != "" || os.Getenv(clientcmd.RecommendedConfigPathEnvVar) != "", kubeconfigPath, kubeContext)
	if kubeconfigPath == "" {
		kubeconfigPath = os.Getenv(clientcmd.RecommendedConfigPathEnvVar)
	}
	if kubeconfigPath != "" {
		rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath}
		overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}
		config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig %q: %w", kubeconfigPath, err)
		}
		log.Printf("[LLDP-AGENT] KUBERNETES CONFIG SUCCESS source=kubeconfig path=%q context=%q apiServer=%q", kubeconfigPath, kubeContext, config.Host)
		return config, nil
	}

	if config, err := rest.InClusterConfig(); err == nil {
		log.Printf("[LLDP-AGENT] KUBERNETES CONFIG SUCCESS source=in-cluster apiServer=%q", config.Host)
		return config, nil
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load default kubeconfig: %w", err)
	}
	log.Printf("[LLDP-AGENT] KUBERNETES CONFIG SUCCESS source=default-kubeconfig context=%q apiServer=%q", kubeContext, config.Host)
	return config, nil
}
