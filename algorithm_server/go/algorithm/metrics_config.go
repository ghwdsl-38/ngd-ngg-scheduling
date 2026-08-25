// metrics_config.go loads the Prometheus query catalogue and HTTP authentication/TLS settings.
package algorithm

import (
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

//go:embed prometheus_metrics.json
var embeddedPrometheusMetrics []byte

type metricDefinition struct {
	Name          string `json:"name"`
	Query         string `json:"query"`
	Unit          string `json:"unit"`
	Normalization string `json:"normalization"`
	Required      bool   `json:"required"`
}

type metricCatalogue struct {
	Version   string             `json:"version"`
	NodeLabel string             `json:"nodeLabel"`
	Metrics   []metricDefinition `json:"metrics"`
}

func loadMetricCatalogue(path string) (metricCatalogue, error) {
	raw := embeddedPrometheusMetrics
	if strings.TrimSpace(path) != "" {
		var err error
		raw, err = os.ReadFile(path)
		if err != nil {
			return metricCatalogue{}, fmt.Errorf("read Prometheus metrics config: %w", err)
		}
	}
	var catalogue metricCatalogue
	if err := json.Unmarshal(raw, &catalogue); err != nil {
		return metricCatalogue{}, fmt.Errorf("decode Prometheus metrics config: %w", err)
	}
	if catalogue.Version == "" || len(catalogue.Metrics) == 0 {
		return metricCatalogue{}, fmt.Errorf("Prometheus metrics config requires version and metrics")
	}
	if catalogue.NodeLabel == "" {
		catalogue.NodeLabel = "node"
	}
	seen := map[string]struct{}{}
	for _, definition := range catalogue.Metrics {
		if definition.Name == "" || definition.Query == "" || definition.Unit == "" {
			return metricCatalogue{}, fmt.Errorf("every Prometheus metric requires name, query and unit")
		}
		if definition.Normalization != "ratio" && definition.Normalization != "non_negative" {
			return metricCatalogue{}, fmt.Errorf("metric %s uses unsupported normalization %q", definition.Name, definition.Normalization)
		}
		if _, exists := seen[definition.Name]; exists {
			return metricCatalogue{}, fmt.Errorf("duplicate Prometheus metric name %s", definition.Name)
		}
		seen[definition.Name] = struct{}{}
	}
	return catalogue, nil
}

func loadBearerToken() (string, error) {
	direct := strings.TrimSpace(os.Getenv("PROMETHEUS_BEARER_TOKEN"))
	path := strings.TrimSpace(os.Getenv("PROMETHEUS_BEARER_TOKEN_FILE"))
	if direct != "" && path != "" {
		return "", fmt.Errorf("PROMETHEUS_BEARER_TOKEN and PROMETHEUS_BEARER_TOKEN_FILE are mutually exclusive")
	}
	if path == "" {
		return direct, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Prometheus bearer token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("Prometheus bearer token file is empty")
	}
	return token, nil
}

func newPrometheusHTTPClient(timeout time.Duration) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if raw := strings.TrimSpace(os.Getenv("PROMETHEUS_INSECURE_SKIP_VERIFY")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("parse PROMETHEUS_INSECURE_SKIP_VERIFY: %w", err)
		}
		// This is explicit opt-in for isolated test environments only.
		tlsConfig.InsecureSkipVerify = value //nolint:gosec
	}
	tlsConfig.ServerName = strings.TrimSpace(os.Getenv("PROMETHEUS_TLS_SERVER_NAME"))
	if path := strings.TrimSpace(os.Getenv("PROMETHEUS_CA_FILE")); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read Prometheus CA file: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("Prometheus CA file contains no valid certificate")
		}
		tlsConfig.RootCAs = roots
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: tlsConfig,
		},
	}, nil
}
