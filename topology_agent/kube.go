package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type nodeObject struct {
	Metadata nodeMetadata `json:"metadata"`
}

type nodeMetadata struct {
	Name            string            `json:"name"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resourceVersion"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
}

type watchEvent struct {
	Type   string     `json:"type"`
	Object nodeObject `json:"object"`
}

type kubeClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func inClusterClient() (*kubeClient, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS")
	if host == "" || port == "" {
		return nil, fmt.Errorf("Kubernetes in-cluster environment is unavailable")
	}
	token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return nil, err
	}
	ca, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("load Kubernetes service account CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	return &kubeClient{baseURL: "https://" + host + ":" + port, token: strings.TrimSpace(string(token)), client: &http.Client{Transport: transport}}, nil
}

func (c *kubeClient) getNode(ctx context.Context, name string) (nodeObject, error) {
	var node nodeObject
	err := c.request(ctx, http.MethodGet, "/api/v1/nodes/"+url.PathEscape(name), nil, &node)
	return node, err
}

func (c *kubeClient) patchNode(ctx context.Context, name string, labels, annotations map[string]string) error {
	body := map[string]any{"metadata": map[string]any{"labels": labels, "annotations": annotations}}
	return c.request(ctx, http.MethodPatch, "/api/v1/nodes/"+url.PathEscape(name), body, nil)
}

func (c *kubeClient) watchNode(ctx context.Context, name, resourceVersion string, handle func(nodeObject) error) error {
	query := url.Values{"watch": {"1"}, "fieldSelector": {"metadata.name=" + name}, "resourceVersion": {resourceVersion}, "timeoutSeconds": {"300"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/nodes?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("watch Node HTTP %d: %s", response.StatusCode, raw)
	}
	decoder := json.NewDecoder(bufio.NewReader(response.Body))
	for {
		var event watchEvent
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if event.Type == "ADDED" || event.Type == "MODIFIED" {
			if err := handle(event.Object); err != nil {
				return err
			}
		}
	}
}

func (c *kubeClient) request(ctx context.Context, method, path string, body, result any) error {
	requestContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(requestContext, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if method == http.MethodPatch {
		request.Header.Set("Content-Type", "application/merge-patch+json")
	} else if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("Kubernetes HTTP %d: %s", response.StatusCode, raw)
	}
	if result != nil {
		return json.NewDecoder(response.Body).Decode(result)
	}
	return nil
}
