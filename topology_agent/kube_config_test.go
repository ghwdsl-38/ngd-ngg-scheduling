package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitKubeconfigBuildsAuthenticatedClient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig")
	content := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: target
  cluster:
    server: %s
users:
- name: lldp
  user:
    token: expected-token
contexts:
- name: selected
  context:
    cluster: target
    user: lldp
current-context: selected
`, "https://kube-api.example.test:6443")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadKubernetesConfig(path, "selected")
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "https://kube-api.example.test:6443" || config.BearerToken != "expected-token" {
		t.Fatalf("kubeconfig was not loaded: host=%q token=%q", config.Host, config.BearerToken)
	}
	if _, err := newKubeClient(config); err != nil {
		t.Fatalf("build authenticated client-go client: %v", err)
	}
}

func TestExplicitMissingKubeconfigDoesNotFallBack(t *testing.T) {
	if _, err := loadKubernetesConfig(filepath.Join(t.TempDir(), "missing"), ""); err == nil {
		t.Fatal("expected explicit missing kubeconfig to fail")
	}
}

func TestKubeconfigEnvironmentAndContextOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig")
	content := `apiVersion: v1
kind: Config
clusters:
- name: first
  cluster:
    server: https://first.example.test:6443
- name: second
  cluster:
    server: https://second.example.test:6443
users:
- name: lldp
  user:
    token: expected-token
contexts:
- name: first
  context: {cluster: first, user: lldp}
- name: second
  context: {cluster: second, user: lldp}
current-context: first
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	config, err := loadKubernetesConfig("", "second")
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "https://second.example.test:6443" {
		t.Fatalf("context override selected host %q", config.Host)
	}
}
