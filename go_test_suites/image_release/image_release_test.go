package image_release_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"demo.ngg/go-test-suites/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	demandGVK = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupDemand"}
	grantGVK  = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupGrant"}
)

// TestReleaseImagesWithEnvtest verifies the packaged amd64 containers rather
// than starting PRC and Algorithm directly from Go source. envtest supplies a
// real kube-apiserver/etcd pair and MockPrometheus supplies the real HTTP API
// shape, bearer authentication, PromQL validation and fourteen Node metrics.
func TestReleaseImagesWithEnvtest(t *testing.T) {
	if testing.Short() {
		t.Skip("release image validation starts Docker containers and envtest")
	}
	root := common.ProjectRoot()
	version := env("RELEASE_VERSION", "v0.6.1")
	repository := env("DOCKERHUB_REPOSITORY", "ghwdsl/ngd-ngg-scheduling")
	prcImage := env("PRC_RELEASE_TEST_IMAGE", repository+":prc-"+version+"-local-amd64")
	algorithmImage := env("ALGORITHM_RELEASE_TEST_IMAGE", repository+":algorithm-"+version+"-local-amd64")
	runDirectory := os.Getenv("IMAGE_VALIDATION_RUN_DIR")
	if runDirectory == "" {
		runDirectory = filepath.Join(root, "image_validation", "results", time.Now().Format("20060102-150405.000000000"))
	}
	actualDirectory := filepath.Join(runDirectory, "actual")
	if err := os.MkdirAll(actualDirectory, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, image := range []string{prcImage, algorithmImage} {
		architecture := strings.TrimSpace(dockerOutput(t, "image", "inspect", image, "--format", "{{.Architecture}}"))
		if architecture != "amd64" {
			t.Fatalf("local test image %s architecture=%q, want amd64", image, architecture)
		}
	}

	fixture, err := common.GenerateFixture(1000)
	if err != nil {
		t.Fatal(err)
	}
	topologyPath := filepath.Join(runDirectory, "network-topology.yaml")
	if err := os.WriteFile(topologyPath, fixture.TopologyConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = common.WriteYAML(filepath.Join(runDirectory, "ngd-input.yaml"), fixture.Demand.Object)

	prometheus, err := common.NewMockPrometheus("image-validation-token", fixture.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(prometheus.Close)
	if err := prometheus.AssertAuthenticationBehavior(); err != nil {
		t.Fatalf("Mock Prometheus authentication behavior: %v", err)
	}

	algorithmPort := freePort(t)
	prcPort := freePort(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	algorithmName := "ngd-ngg-algorithm-validation-" + suffix
	prcName := "ngd-ngg-prc-validation-" + suffix
	startContainer(t, algorithmName, filepath.Join(actualDirectory, "algorithm-container.log"),
		"--network=host",
		"-e", fmt.Sprintf("ALGORITHM_LISTEN_ADDRESS=127.0.0.1:%d", algorithmPort),
		"-e", "PROMETHEUS_URL="+prometheus.URL(),
		"-e", "PROMETHEUS_BEARER_TOKEN=image-validation-token",
		"-e", "PROMETHEUS_REFRESH_SECONDS=1",
		"-e", "PROMETHEUS_STALE_SECONDS=120",
		"-e", "TOPOLOGY_CONFIG_FILE=/validation/network-topology.yaml",
		"-v", topologyPath+":/validation/network-topology.yaml:ro",
		algorithmImage,
	)
	algorithmURL := fmt.Sprintf("http://127.0.0.1:%d", algorithmPort)
	waitHTTP(t, algorithmName, algorithmURL+"/healthz", 30*time.Second, nil)
	waitHTTP(t, algorithmName, algorithmURL+"/internal/v1/cache/status", 30*time.Second, func(value map[string]any) bool {
		metrics, _ := value["metrics"].(map[string]any)
		ready, _ := metrics["ready"].(bool)
		return ready
	})

	environment := common.StartEnvTest(t)
	t.Cleanup(func() { environment.Stop(t) })
	apiClient, err := client.New(environment.Config, client.Options{Scheme: environment.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	setupContext, setupCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer setupCancel()
	if err := common.CreateKubernetesInputs(setupContext, apiClient, fixture); err != nil {
		t.Fatal(err)
	}
	// Kubeconfig contains an ephemeral client private key. Keep it in Go's
	// temporary directory so it is removed after the test and never retained
	// with the human-readable validation evidence.
	kubeconfigPath := filepath.Join(t.TempDir(), "envtest.kubeconfig")
	if err := writeKubeconfig(kubeconfigPath, environment.Config); err != nil {
		t.Fatal(err)
	}
	_ = common.WriteJSON(filepath.Join(runDirectory, "envtest-connection-summary.json"), map[string]any{
		"server": environment.Config.Host, "authentication": "ephemeral-client-certificate", "credentialsPersisted": false,
	})

	startContainer(t, prcName, filepath.Join(actualDirectory, "prc-container.log"),
		"--network=host",
		"-e", "KUBECONFIG=/validation/envtest.kubeconfig",
		"-e", "ALGORITHM_URL="+algorithmURL,
		"-e", "CLUSTER_ID=mock-1000-node-cluster",
		"-v", kubeconfigPath+":/validation/envtest.kubeconfig:ro",
		prcImage,
		"--leader-elect=false",
		"--metrics-bind-address=0",
		fmt.Sprintf("--health-probe-bind-address=127.0.0.1:%d", prcPort),
		"--demand-refresh-interval=15s",
		"--max-concurrent-refreshes=5",
	)
	waitHTTP(t, prcName, fmt.Sprintf("http://127.0.0.1:%d/readyz", prcPort), 45*time.Second, nil)
	waitHTTP(t, algorithmName, algorithmURL+"/internal/v1/node-static-cache/status", 45*time.Second, func(value map[string]any) bool {
		ready, _ := value["ready"].(bool)
		count, _ := value["nodeCount"].(float64)
		return ready && int(count) == fixture.NodeCount
	})

	demand := fixture.Demand.DeepCopy()
	demand.SetUID("")
	demand.SetResourceVersion("")
	demand.SetCreationTimestamp(metav1.Time{})
	demand.SetGroupVersionKind(demandGVK)
	started := time.Now()
	if err := apiClient.Create(setupContext, demand); err != nil {
		t.Fatalf("create NGD in envtest: %v", err)
	}

	waitContext, waitCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer waitCancel()
	grant := &unstructured.Unstructured{}
	grant.SetGroupVersionKind(grantGVK)
	if err := common.Eventually(waitContext, 20*time.Millisecond, func(ctx context.Context) (bool, error) {
		if err := apiClient.Get(ctx, client.ObjectKey{Name: "ngg-go-test-demand"}, grant); err != nil {
			return false, err
		}
		phase, _, _ := unstructured.NestedString(grant.Object, "status", "phase")
		return phase == "Active", nil
	}); err != nil {
		t.Fatalf("wait for NGG Active: %v", err)
	}
	elapsed := time.Since(started)

	finalDemand := &unstructured.Unstructured{}
	finalDemand.SetGroupVersionKind(demandGVK)
	if err := common.Eventually(waitContext, 20*time.Millisecond, func(ctx context.Context) (bool, error) {
		if err := apiClient.Get(ctx, client.ObjectKey{Name: "go-test-demand"}, finalDemand); err != nil {
			return false, err
		}
		phase, _, _ := unstructured.NestedString(finalDemand.Object, "status", "phase")
		return phase == "Fulfilled", nil
	}); err != nil {
		t.Fatalf("wait for NGD Fulfilled: %v", err)
	}

	nodes, found, err := unstructured.NestedSlice(grant.Object, "spec", "nodes")
	if err != nil || !found || len(nodes) == 0 {
		t.Fatalf("packaged PRC created invalid NGG nodes: found=%v count=%d err=%v", found, len(nodes), err)
	}
	requests := prometheus.Requests()
	metricNames := map[string]struct{}{}
	for _, request := range requests {
		if request.StatusCode == http.StatusOK && request.Authorization == "Bearer image-validation-token" {
			metricNames[request.MetricName] = struct{}{}
		}
	}
	if len(metricNames) != 14 {
		t.Fatalf("packaged Algorithm queried %d authenticated metrics, want 14", len(metricNames))
	}
	assertContainerLogsContain(t, prcName, filepath.Join(actualDirectory, "prc-container.log"), []string{
		"observed NGD event",
		"publishing Node static snapshot to Algorithm Server",
		"calling Algorithm Server",
		"received Algorithm Server result",
		"updated NGG status",
		"published formal platform NGG",
	})
	assertContainerLogsContain(t, algorithmName, filepath.Join(actualDirectory, "algorithm-container.log"), []string{
		"event=topology_loaded",
		"event=prometheus_refresh_completed status=success",
		"event=static_snapshot_accepted",
		"event=allocation_received",
		"event=topology_resolution_completed",
		"event=python_worker_jsonl_started",
		"event=allocation_completed",
	})

	cache := fetchJSON(t, algorithmURL+"/internal/v1/cache/status")
	_ = common.WriteYAML(filepath.Join(actualDirectory, "ngg.yaml"), grant.Object)
	_ = common.WriteYAML(filepath.Join(actualDirectory, "ngd-status.yaml"), map[string]any{"status": finalDemand.Object["status"]})
	_ = common.WriteJSON(filepath.Join(actualDirectory, "algorithm-cache.json"), cache)
	_ = common.WriteJSON(filepath.Join(actualDirectory, "mock-prometheus-requests.json"), requests)
	_ = common.WriteJSON(filepath.Join(runDirectory, "summary.json"), map[string]any{
		"status": "PASS", "version": version, "platformTested": "linux/amd64",
		"prcImage": prcImage, "algorithmImage": algorithmImage,
		"nodeCount": fixture.NodeCount, "prometheusMetricCount": len(metricNames),
		"nggNodeCount": len(nodes), "ngdToNGGElapsedMs": float64(elapsed.Microseconds()) / 1000,
	})
	t.Logf("packaged containers PASS nodes=%d metrics=%d elapsedMs=%.3f results=%s", fixture.NodeCount, len(metricNames), float64(elapsed.Microseconds())/1000, runDirectory)
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func dockerOutput(t *testing.T, arguments ...string) string {
	t.Helper()
	output, err := exec.Command("docker", arguments...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func startContainer(t *testing.T, name, logPath string, arguments ...string) {
	t.Helper()
	args := append([]string{"run", "--detach", "--name", name}, arguments...)
	dockerOutput(t, args...)
	t.Cleanup(func() {
		logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
		_ = os.WriteFile(logPath, logs, 0o644)
		_, _ = exec.Command("docker", "rm", "--force", name).CombinedOutput()
	})
}

func assertContainerLogsContain(t *testing.T, name, logPath string, markers []string) {
	t.Helper()
	logs := dockerOutput(t, "logs", name)
	if err := os.WriteFile(logPath, []byte(logs), 0o644); err != nil {
		t.Fatalf("write %s logs: %v", name, err)
	}
	for _, marker := range markers {
		if !strings.Contains(logs, marker) {
			t.Fatalf("container %s logs do not contain %q\nlogs:\n%s", name, marker, logs)
		}
	}
}

func waitHTTP(t *testing.T, containerName, endpoint string, timeout time.Duration, accept func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastError error
	for time.Now().Before(deadline) {
		if accept == nil {
			if err := probeHTTP(endpoint); err == nil {
				return map[string]any{"statusCode": http.StatusOK}
			} else {
				lastError = err
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		value, err := fetchJSONResult(endpoint)
		if err == nil && accept(value) {
			return value
		}
		lastError = err
		time.Sleep(100 * time.Millisecond)
	}
	logs, _ := exec.Command("docker", "logs", containerName).CombinedOutput()
	t.Fatalf("wait for %s: lastError=%v\ncontainer logs:\n%s", endpoint, lastError, logs)
	return nil
}

func probeHTTP(endpoint string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s returned %d", endpoint, response.StatusCode)
	}
	return nil
}

func fetchJSON(t *testing.T, endpoint string) map[string]any {
	t.Helper()
	value, err := fetchJSONResult(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func fetchJSONResult(endpoint string) (map[string]any, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return nil, fmt.Errorf("GET %s returned %d: %s", endpoint, response.StatusCode, body)
	}
	var value map[string]any
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func writeKubeconfig(path string, config *rest.Config) error {
	cluster := &clientcmdapi.Cluster{
		Server:                   config.Host,
		CertificateAuthorityData: config.CAData,
		InsecureSkipTLSVerify:    config.Insecure,
	}
	auth := &clientcmdapi.AuthInfo{
		ClientCertificateData: config.CertData,
		ClientKeyData:         config.KeyData,
		Token:                 config.BearerToken,
		Username:              config.Username,
		Password:              config.Password,
	}
	value := clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{"envtest": cluster},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{"envtest": auth},
		Contexts:       map[string]*clientcmdapi.Context{"envtest": {Cluster: "envtest", AuthInfo: "envtest"}},
		CurrentContext: "envtest",
	}
	raw, err := clientcmd.Write(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}
