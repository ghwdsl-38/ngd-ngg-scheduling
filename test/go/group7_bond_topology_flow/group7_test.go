package group7_bond_topology_flow_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	algorithm "demo.ngg/algorithm-server/algorithm"
	"demo.ngg/go-test-suites/common"
	bonddiscovery "demo.ngg/topology-agent/pkg/bond"
	"demo.ngg/topology-agent/pkg/topologyfacts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	prcapp "scheduling.demo.ngg.io/prc/pkg/application"
	"scheduling.demo.ngg.io/prc/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	leafA = "HB-HL-DC1-102-C03-44U-LTY-CSQ-LEAF-SW01-ZTE5960X"
	leafB = "HB-HL-DC1-102-C04-44U-LTY-CSQ-LEAF-SW02-ZTE5960X"
)

var (
	demandGVK = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupDemand"}
	grantGVK  = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupGrant"}
)

type scenarioInput struct {
	Scenario    string `yaml:"scenario"`
	CaptureBond bool   `yaml:"captureBond"`
	Bond        struct {
		Name        string   `yaml:"name"`
		Mode        string   `yaml:"mode"`
		Slaves      []string `yaml:"slaves"`
		ActiveSlave string   `yaml:"activeSlave"`
	} `yaml:"bond"`
	Interfaces map[string]struct {
		Carrier   string `yaml:"carrier"`
		Operstate string `yaml:"operstate"`
	} `yaml:"interfaces"`
	Neighbors map[string]struct {
		LeafSwitchID string `yaml:"leafSwitchId"`
		RemotePortID string `yaml:"remotePortId"`
	} `yaml:"neighbors"`
}

type preparedTopology struct {
	BondMode           string
	SelectedInterfaces []string
	LeafSwitchIDs      []string
	NodeEvidence       []map[string]any
}

// TestGroup7 validates both production Bond policies through the complete
// post-LLDP scheduling chain: Bond selection -> Node metadata -> PRC static
// snapshot -> real Go Algorithm -> Python Worker -> formal NGG in envtest.
func TestGroup7_BondModesFullFlow(t *testing.T) {
	groupDirectory := currentGroupDirectory(t)
	runDirectory := common.NewRunDirectory(t, groupDirectory)
	debugMode := os.Getenv("NGG_TEST_DEBUG") == "true"
	for _, scenario := range []string{"active-backup", "load-balance", "bond-master-active-backup", "bond-master-load-balance"} {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			inputPath := filepath.Join(groupDirectory, "testdata", scenario, "input", "bond-topology.yaml")
			input := readScenario(t, inputPath)
			fixture, err := common.GenerateFixture(20)
			if err != nil {
				t.Fatal(err)
			}
			fixture.TopologyConfig = []byte(unicomPeerTopology)
			fixture.Pods = nil
			prepared := prepareBondTopology(t, input, fixture)
			scenarioRun := filepath.Join(runDirectory, scenario)
			if err := os.MkdirAll(filepath.Join(scenarioRun, "comparison"), 0o755); err != nil {
				t.Fatal(err)
			}
			runFullFlow(t, fixture, input, prepared, scenarioRun, filepath.Join(groupDirectory, "testdata", scenario, "expected", "summary.json"), debugMode)
		})
	}
}

// TestGroup7_DualLinksToSameLeafAreDeduplicated protects the boundary between
// physical links and topology members: two Bond slaves do not imply two Leaf
// switches when both observations resolve to the same upstream Leaf.
func TestGroup7_DualLinksToSameLeafAreDeduplicated(t *testing.T) {
	observed, labels, annotations, err := topologyfacts.BuildNodeMetadata(topologyfacts.Observation{
		Source: "MockLLDP",
		Links: []topologyfacts.Link{
			{BondName: "bond0", BondMode: "802.3ad", Interface: "eth0", LeafSwitchID: leafA, ChassisID: "00:11:22:33:44:55", RemotePortID: "port-1", Active: true},
			{BondName: "bond0", BondMode: "802.3ad", Interface: "eth1", LeafSwitchID: leafA, ChassisID: "00:11:22:33:44:55", RemotePortID: "port-2", Active: true},
		},
	}, time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.Links) != 2 || len(observed.LeafSwitchIDs) != 1 {
		t.Fatalf("two links to the same Leaf must remain two links and one Leaf: %#v", observed)
	}
	if labels[topologyfacts.Prefix+"leaf-count"] != "1" {
		t.Fatalf("unexpected leaf-count: %#v", labels)
	}
	if !strings.Contains(fmt.Sprint(annotations[topologyfacts.Prefix+"leaf-switch-ids"]), leafA) {
		t.Fatalf("Leaf annotation does not contain expected switch: %#v", annotations)
	}
}

func readScenario(t *testing.T, path string) scenarioInput {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result scenarioInput
	if err := yaml.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return result
}

func prepareBondTopology(t *testing.T, input scenarioInput, fixture *common.Fixture) preparedTopology {
	t.Helper()
	sysfs := t.TempDir()
	bonding := filepath.Join(sysfs, input.Bond.Name, "bonding")
	mustMkdir(t, bonding)
	mustWrite(t, filepath.Join(bonding, "mode"), input.Bond.Mode)
	mustWrite(t, filepath.Join(bonding, "slaves"), strings.Join(input.Bond.Slaves, " "))
	mustWrite(t, filepath.Join(bonding, "active_slave"), input.Bond.ActiveSlave)
	for name, state := range input.Interfaces {
		path := filepath.Join(sysfs, name)
		mustMkdir(t, path)
		mustMkdir(t, filepath.Join(path, "device"))
		mustWrite(t, filepath.Join(path, "type"), "1")
		mustWrite(t, filepath.Join(path, "carrier"), state.Carrier)
		mustWrite(t, filepath.Join(path, "operstate"), state.Operstate)
	}
	mustWrite(t, filepath.Join(sysfs, input.Bond.Name, "carrier"), "1")
	mustWrite(t, filepath.Join(sysfs, input.Bond.Name, "operstate"), "up")
	var scope map[string]struct{}
	if !input.CaptureBond {
		scope = map[string]struct{}{}
		for name, state := range input.Interfaces {
			if strings.HasPrefix(input.Bond.Mode, "active-backup") && name != input.Bond.ActiveSlave {
				continue
			}
			if state.Carrier == "1" || state.Operstate == "up" {
				scope[name] = struct{}{}
			}
		}
	}
	selected, err := bonddiscovery.SelectInterfacesAt(sysfs, t.TempDir(), scope)
	if err != nil {
		t.Fatalf("production Bond discovery: %v", err)
	}
	selections := bonddiscovery.Sorted(selected)
	if len(selections) == 0 {
		t.Fatal("Bond discovery selected no interface")
	}
	interfaces := make([]string, 0, len(selections))
	leaves := make([]string, 0, len(selections))
	links := make([]topologyfacts.Link, 0, len(selections))
	for _, selection := range selections {
		interfaces = append(interfaces, selection.Name)
		neighborNames := []string{selection.Name}
		if input.CaptureBond {
			if selection.Name != input.Bond.Name || selection.Kind != "bond-master" || selection.Active {
				t.Fatalf("invalid Bond master observation: %#v", selection)
			}
			neighborNames = nil
			for name := range input.Neighbors {
				neighborNames = append(neighborNames, name)
			}
			sort.Strings(neighborNames)
		}
		for _, name := range neighborNames {
			neighbor, ok := input.Neighbors[name]
			if !ok || neighbor.LeafSwitchID == "" {
				t.Fatalf("missing Mock LLDP neighbor for %s", name)
			}
			leaves = append(leaves, neighbor.LeafSwitchID)
			links = append(links, topologyfacts.Link{
				BondName: input.Bond.Name, BondMode: selection.BondMode,
				Interface: selection.Name, LeafSwitchID: neighbor.LeafSwitchID,
				RemotePortID: neighbor.RemotePortID, Active: selection.Active,
			})
		}
	}
	observation, labelsPatch, annotationsPatch, err := topologyfacts.BuildNodeMetadata(
		topologyfacts.Observation{Links: links, Source: "MockLLDP"},
		time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("production topology metadata builder: %v", err)
	}
	leaves = observation.LeafSwitchIDs
	evidence := make([]map[string]any, 0, len(fixture.Nodes))
	for index := range fixture.Nodes {
		node := &fixture.Nodes[index]
		node.Spec.Unschedulable = false
		if node.Labels == nil {
			node.Labels = map[string]string{}
		}
		for key, value := range labelsPatch {
			if value == nil {
				delete(node.Labels, key)
				continue
			}
			node.Labels[key] = fmt.Sprint(value)
		}
		if node.Annotations == nil {
			node.Annotations = map[string]string{}
		}
		for key, value := range annotationsPatch {
			node.Annotations[key] = fmt.Sprint(value)
		}
		evidence = append(evidence, map[string]any{
			"nodeName": node.Name, "leafSetId": node.Labels["topology.demo.ngg.io/leaf-set-id"],
			"leafSwitchIds": leaves, "links": observation.Links,
		})
	}
	return preparedTopology{BondMode: selections[0].BondMode, SelectedInterfaces: interfaces, LeafSwitchIDs: leaves, NodeEvidence: evidence}
}

func runFullFlow(t *testing.T, fixture *common.Fixture, input scenarioInput, prepared preparedTopology, runDirectory, expectedPath string, debugMode bool) {
	t.Helper()
	setupTimeout, waitTimeout, shutdownTimeout := 90*time.Second, 30*time.Second, 5*time.Second
	metricsStaleAfter := 2 * time.Minute
	algorithmTimeoutSeconds := int64(5)
	if debugMode {
		debugTimeout := 10 * time.Minute
		if configured := os.Getenv("NGG_TEST_DEBUG_TIMEOUT"); configured != "" {
			parsed, err := time.ParseDuration(configured)
			if err != nil || parsed < time.Second {
				t.Fatalf("invalid NGG_TEST_DEBUG_TIMEOUT %q", configured)
			}
			debugTimeout = parsed
		}
		setupTimeout, waitTimeout, shutdownTimeout = debugTimeout, debugTimeout, debugTimeout
		metricsStaleAfter = 3 * debugTimeout
		algorithmTimeoutSeconds = int64(debugTimeout / time.Second)
		t.Logf("Group7 debug mode: internal timeouts=%s; elapsed is not a performance result", debugTimeout)
	}
	prometheus, err := common.NewMockPrometheus("group7-prometheus-token", fixture.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	defer prometheus.Close()
	worker := common.PythonWorkerConfig(filepath.Join(runDirectory, "actual", "python-worker"))
	algorithmServer, err := algorithm.NewServer(algorithm.Config{
		BootID: "group7-" + input.Scenario, PrometheusURL: prometheus.URL(),
		PrometheusBearerToken: "group7-prometheus-token", PrometheusClient: prometheus.Client(),
		MetricsStaleAfter: metricsStaleAfter, TopologyConfigData: fixture.TopologyConfig,
		PythonExecutable: worker.Executable, PythonModule: worker.Module,
		PythonPath: worker.PythonPath, WorkerEvidenceDir: worker.EvidenceDir,
	}, algorithm.ServerOptions{ListenAddress: "127.0.0.1:0", ShutdownTimeout: shutdownTimeout})
	if err != nil {
		t.Fatalf("create Algorithm Server: %v", err)
	}
	algorithmContext, algorithmCancel := context.WithCancel(context.Background())
	if err := algorithmServer.Start(algorithmContext); err != nil {
		t.Fatalf("start Algorithm Server: %v", err)
	}
	defer func() {
		algorithmCancel()
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := algorithmServer.Close(ctx); err != nil {
			t.Errorf("close Algorithm Server: %v", err)
		}
		if err := algorithmServer.Wait(); err != nil {
			t.Errorf("wait Algorithm Server: %v", err)
		}
	}()

	setupContext, setupCancel := context.WithTimeout(context.Background(), setupTimeout)
	defer setupCancel()
	if err := algorithmServer.WaitForReady(setupContext); err != nil {
		t.Fatalf("wait Algorithm Server ready: %v", err)
	}
	environment := common.StartEnvTest(t)
	defer environment.Stop(t)
	apiClient, err := client.New(environment.Config, client.Options{Scheme: environment.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	if err := common.CreateKubernetesInputs(setupContext, apiClient, fixture); err != nil {
		t.Fatal(err)
	}

	var exchangeMu sync.Mutex
	exchanges := []controller.AlgorithmExchange{}
	reconcileStarted := make(chan time.Time, 1)
	var startOnce sync.Once
	prcApplication, err := prcapp.New(prcapp.Config{
		KubernetesConfig: environment.Config, Scheme: environment.Scheme,
		AlgorithmURL: algorithmServer.URL(), ClusterID: "group7-20-node-cluster",
		HTTPClient: algorithmServer.HTTPClient(), MetricsBindAddress: "0",
		HealthProbeBindAddress: "0", LeaderElection: false, DebugAlgorithmTrace: true,
		SkipControllerNameValidation: true,
		AlgorithmRecorder: func(exchange controller.AlgorithmExchange) {
			exchangeMu.Lock()
			exchanges = append(exchanges, exchange)
			exchangeMu.Unlock()
		},
		ReconcileObserver: func(_ string, _ int64, observedAt time.Time) {
			startOnce.Do(func() { reconcileStarted <- observedAt })
		},
	})
	if err != nil {
		t.Fatalf("create PRC Application: %v", err)
	}
	managerContext, managerCancel := context.WithCancel(context.Background())
	managerErrors := make(chan error, 1)
	go func() { managerErrors <- prcApplication.Start(managerContext) }()
	defer func() {
		managerCancel()
		select {
		case err := <-managerErrors:
			if err != nil {
				t.Errorf("manager stopped: %v", err)
			}
		case <-time.After(shutdownTimeout):
			t.Error("manager did not stop")
		}
	}()
	if _, err := prcApplication.WaitForReady(setupContext); err != nil {
		t.Fatalf("PRC Application ready: %v", err)
	}

	demand := fixture.Demand.DeepCopy()
	demand.SetUID("")
	demand.SetResourceVersion("")
	demand.SetCreationTimestamp(metav1.Time{})
	demand.SetGroupVersionKind(demandGVK)
	if debugMode {
		spec, _, _ := unstructured.NestedMap(demand.Object, "spec")
		policy, _, _ := unstructured.NestedMap(spec, "grantPolicy")
		if policy == nil {
			policy = map[string]any{}
		}
		policy["algorithmTimeoutSeconds"] = algorithmTimeoutSeconds
		spec["grantPolicy"] = policy
		if err := unstructured.SetNestedMap(demand.Object, spec, "spec"); err != nil {
			t.Fatal(err)
		}
	}
	if err := apiClient.Create(setupContext, demand); err != nil {
		t.Fatalf("create NGD: %v", err)
	}
	waitContext, waitCancel := context.WithTimeout(context.Background(), waitTimeout)
	defer waitCancel()
	var started time.Time
	select {
	case started = <-reconcileStarted:
	case <-waitContext.Done():
		t.Fatalf("wait PRC to observe NGD: %v", waitContext.Err())
	}
	grant := waitForGrant(t, waitContext, apiClient)
	elapsed := time.Since(started)
	if debugMode {
		_ = common.WriteJSON(filepath.Join(runDirectory, "debug-timing.json"), map[string]any{
			"debug": true, "performanceValid": false, "unit": "ms",
			"boundary":  "PRC observes NGD -> real Algorithm/Python -> NGG Active",
			"elapsedMs": float64(elapsed.Microseconds()) / 1000,
		})
	} else {
		common.WriteTiming(t, runDirectory, "PRC observes NGD -> real Algorithm/Python -> NGG Active", elapsed)
	}

	exchangeMu.Lock()
	exchangeCopy := append([]controller.AlgorithmExchange(nil), exchanges...)
	exchangeMu.Unlock()
	staticRequest := exchangeBody(t, exchangeCopy, "PUT", "/internal/v1/node-static-snapshots/", false)
	algorithmRequest := exchangeBody(t, exchangeCopy, "POST", "/api/v1/allocate", false)
	algorithmResponse := exchangeBody(t, exchangeCopy, "POST", "/api/v1/allocate", true)
	staticNodes, ok := staticRequest["nodes"].([]any)
	if !ok || len(staticNodes) != fixture.NodeCount {
		t.Fatalf("static Node count=%d, want %d", len(staticNodes), fixture.NodeCount)
	}
	staticLeafCount := assertStaticLeafSets(t, staticNodes, prepared.LeafSwitchIDs)
	groups, ok := algorithmResponse["candidateNodeGroups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("Algorithm group count=%d, want 1 Leaf Domain", len(groups))
	}
	group := groups[0].(map[string]any)
	algorithmNodes := group["nodes"].([]any)
	grantNodes, _, _ := unstructured.NestedSlice(grant.Object, "spec", "nodes")
	if len(algorithmNodes) != 10 || len(grantNodes) != 10 {
		t.Fatalf("Algorithm/NGG Node counts=%d/%d, want 10/10", len(algorithmNodes), len(grantNodes))
	}

	summary := map[string]any{
		"scenario": input.Scenario, "bondMode": prepared.BondMode,
		"selectedInterfaces": prepared.SelectedInterfaces, "persistedLeafSwitchIds": prepared.LeafSwitchIDs,
		"staticNodeCount": len(staticNodes), "staticNodeLeafCount": staticLeafCount,
		"algorithmGroupCount": len(groups), "algorithmTopologyLevel": fmt.Sprint(group["topologyLevel"]),
		"algorithmNodeCount": len(algorithmNodes), "algorithmNodesUnique": uniqueNodeRefs(algorithmNodes, "nodeUID"),
		"nggNodeCount": len(grantNodes), "nggNodesUnique": uniqueNodeRefs(grantNodes, "name"),
	}
	actualDirectory := filepath.Join(runDirectory, "actual")
	_ = common.WriteYAML(filepath.Join(actualDirectory, "01-bond-and-mock-lldp-input.yaml"), input)
	_ = common.WriteYAML(filepath.Join(actualDirectory, "02-node-leaf-metadata.yaml"), prepared.NodeEvidence)
	_ = common.WriteJSON(filepath.Join(actualDirectory, "03-prc-static-snapshot.json"), staticRequest)
	_ = common.WriteJSON(filepath.Join(actualDirectory, "04-prc-algorithm-request.json"), algorithmRequest)
	_ = common.WriteJSON(filepath.Join(actualDirectory, "05-algorithm-response.json"), algorithmResponse)
	_ = common.WriteYAML(filepath.Join(actualDirectory, "06-ngg.yaml"), grant.Object)
	_ = common.WriteJSON(filepath.Join(actualDirectory, "07-prometheus-requests.json"), prometheus.Requests())
	common.CompareGolden(t, expectedPath, filepath.Join(actualDirectory, "summary.json"), filepath.Join(runDirectory, "comparison", "diff.txt"), summary)
}

func waitForGrant(t *testing.T, ctx context.Context, apiClient client.Client) *unstructured.Unstructured {
	t.Helper()
	var result *unstructured.Unstructured
	err := common.Eventually(ctx, 10*time.Millisecond, func(ctx context.Context) (bool, error) {
		grant := &unstructured.Unstructured{}
		grant.SetGroupVersionKind(grantGVK)
		if err := apiClient.Get(ctx, client.ObjectKey{Name: "ngg-go-test-demand"}, grant); err != nil {
			return false, err
		}
		phase, _, _ := unstructured.NestedString(grant.Object, "status", "phase")
		if phase == "Active" {
			result = grant.DeepCopy()
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("wait NGG Active: %v", err)
	}
	return result
}

func exchangeBody(t *testing.T, exchanges []controller.AlgorithmExchange, method, path string, response bool) map[string]any {
	t.Helper()
	for index := len(exchanges) - 1; index >= 0; index-- {
		item := exchanges[index]
		if item.Method != method || !strings.HasPrefix(item.Path, path) {
			continue
		}
		raw := item.Request
		if response {
			raw = item.Response
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode %s %s exchange: %v", method, path, err)
		}
		return body
	}
	t.Fatalf("exchange not found: %s %s", method, path)
	return nil
}

func assertStaticLeafSets(t *testing.T, nodes []any, expected []string) int {
	t.Helper()
	for _, raw := range nodes {
		node := raw.(map[string]any)
		topology := node["topology"].(map[string]any)
		values := topology["leafSwitchIds"].([]any)
		actual := make([]string, 0, len(values))
		for _, value := range values {
			actual = append(actual, fmt.Sprint(value))
		}
		if strings.Join(actual, "\x00") != strings.Join(expected, "\x00") {
			t.Fatalf("Node %v Leaf set=%v, want %v", node["nodeName"], actual, expected)
		}
	}
	return len(expected)
}

func uniqueNodeRefs(values []any, field string) bool {
	seen := map[string]struct{}{}
	for _, raw := range values {
		value := fmt.Sprint(raw.(map[string]any)[field])
		if value == "" {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func currentGroupDirectory(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve Group7 directory")
	}
	return filepath.Dir(source)
}

const unicomPeerTopology = `
version: group7-unicom-bond-v1
scopes:
  regions:
    - {id: CN-NORTH, name: 华北}
  locations:
    - {id: HB-HL, name: 怀来, regionId: CN-NORTH}
  dataCenters:
    - {id: HB-HL-DC1, locationId: HB-HL}
  rooms:
    - {id: HB-HL-DC1-102, name: 102机房, dataCenterId: HB-HL-DC1}
borderDomains:
  HB-HL-DC1-102-BORDER-DOMAIN-01:
    roomId: HB-HL-DC1-102
    mode: exact-set
    members:
      - HB-HL-DC1-102-C03-02U-LTY-CSQ-BORDER-SW01-ZTE9904X
      - HB-HL-DC1-102-D03-02U-LTY-CSQ-BORDER-SW02-ZTE9904X
leafMetrics:
  ` + leafA + `: {bandwidthGbps: 100, latencyMillis: 0.8}
  ` + leafB + `: {bandwidthGbps: 100, latencyMillis: 0.8}
topology:
  HB-HL-DC1-102:
    ` + leafA + `:
      SPINE: {}
      BORDER:
        HB-HL-DC1-102-C03-02U-LTY-CSQ-BORDER-SW01-ZTE9904X: {local_port: cgei-0/1/1/51, peer_port: cgei-0/3/0/9}
        HB-HL-DC1-102-D03-02U-LTY-CSQ-BORDER-SW02-ZTE9904X: {local_port: cgei-0/1/1/53, peer_port: cgei-0/3/0/9}
      LEAF:
        ` + leafB + `: {local_port: cgei-0/1/1/54, peer_port: cgei-0/1/1/54}
    ` + leafB + `:
      SPINE: {}
      BORDER:
        HB-HL-DC1-102-C03-02U-LTY-CSQ-BORDER-SW01-ZTE9904X: {local_port: cgei-0/1/1/51, peer_port: cgei-0/3/0/10}
        HB-HL-DC1-102-D03-02U-LTY-CSQ-BORDER-SW02-ZTE9904X: {local_port: cgei-0/1/1/53, peer_port: cgei-0/3/0/10}
      LEAF:
        ` + leafA + `: {local_port: cgei-0/1/1/54, peer_port: cgei-0/1/1/54}
`
