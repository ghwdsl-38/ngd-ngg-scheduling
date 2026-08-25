// group2 runner starts a real envtest API/etcd and the real PRC Manager.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"

	"scheduling.demo.ngg.io/ngg-consumer/formalgrant"
	"scheduling.demo.ngg.io/prc/pkg/controller"
)

var (
	formalDemandGVK = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupDemand"}
	formalGrantGVK  = schema.GroupVersionKind{Group: "scheduling.platform.example.io", Version: "v1alpha1", Kind: "NodeGroupGrant"}
)

type fixtureFile struct {
	Items []json.RawMessage `json:"items"`
}

type caseResult struct {
	ID         string         `json:"id"`
	Status     string         `json:"status"`
	Timed      bool           `json:"timed"`
	DurationMS *float64       `json:"durationMs,omitempty"`
	Output     map[string]any `json:"output"`
}

func measured(value float64) *float64 { return &value }

type mockAlgorithm struct {
	mu       sync.Mutex
	bootID   string
	staticID string
	static   map[string]map[string]any
}

type exchangeRecorder struct {
	mu       sync.Mutex
	caseID   string
	identity map[string]string
	root     string
	buffer   map[string][]controller.AlgorithmExchange
}

func (r *exchangeRecorder) setCase(caseID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.caseID = caseID
}

func (r *exchangeRecorder) registerIdentity(uid, caseID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.identity == nil {
		r.identity = map[string]string{}
	}
	r.identity[uid] = caseID
}

func (r *exchangeRecorder) record(exchange controller.AlgorithmExchange) {
	r.mu.Lock()
	defer r.mu.Unlock()
	caseID := r.caseID
	var requestBody map[string]any
	if json.Unmarshal(exchange.Request, &requestBody) == nil {
		if mapped := r.identity[text(requestBody["ngdUID"])]; mapped != "" {
			caseID = mapped
		}
	}
	if caseID == "" {
		caseID = "environment"
	}
	if r.buffer == nil {
		r.buffer = map[string][]controller.AlgorithmExchange{}
	}
	// 计时区间内只做内存复制，不执行JSON序列化和文件写入。
	exchange.Request = append([]byte(nil), exchange.Request...)
	exchange.Response = append([]byte(nil), exchange.Response...)
	r.buffer[caseID] = append(r.buffer[caseID], exchange)
}

func (r *exchangeRecorder) flushAll() {
	r.mu.Lock()
	buffer := r.buffer
	r.buffer = map[string][]controller.AlgorithmExchange{}
	r.mu.Unlock()
	for caseID, items := range buffer {
		for index, exchange := range items {
			r.writeExchange(caseID, index+1, exchange)
		}
	}
}

func (r *exchangeRecorder) writeExchange(caseID string, sequence int, exchange controller.AlgorithmExchange) {
	directory := filepath.Join(r.root, caseID, "process")
	inputName, outputName := "prc-allocation-request.json", "algorithm-result.json"
	if exchange.Method == http.MethodPut {
		inputName, outputName = "prc-static-snapshot-request.json", "prc-static-snapshot-response.json"
	}
	writeRawJSON(filepath.Join(directory, inputName), exchange.Request)
	if len(exchange.Response) > 0 {
		writeRawJSON(filepath.Join(directory, outputName), exchange.Response)
	}
	appendJSONLine(filepath.Join(directory, "prc-algorithm-http.jsonl"), map[string]any{
		"sequence": sequence,
		"from":     "PRC", "to": "Algorithm API Server", "method": exchange.Method,
		"path": exchange.Path, "statusCode": exchange.StatusCode,
		"durationMs": float64(exchange.Duration.Microseconds()) / 1000,
	})
}

type watchTracker struct {
	mu     sync.Mutex
	starts map[string]time.Time
}

func watchKey(uid string, generation int64) string {
	return fmt.Sprintf("%s/%d", uid, generation)
}

func (w *watchTracker) observe(uid string, generation int64, observedAt time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := watchKey(uid, generation)
	if _, exists := w.starts[key]; !exists {
		w.starts[key] = observedAt
	}
}

func (w *watchTracker) wait(uid string, generation int64, timeout time.Duration) time.Time {
	deadline := time.Now().Add(timeout)
	key := watchKey(uid, generation)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		started := w.starts[key]
		w.mu.Unlock()
		if !started.IsZero() {
			return started
		}
		time.Sleep(5 * time.Millisecond)
	}
	fatalf("timeout waiting for PRC Watch start uid=%s generation=%d", uid, generation)
	return time.Time{}
}

func (m *mockAlgorithm) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPut && strings.HasPrefix(request.URL.Path, "/internal/v1/node-static-snapshots/") {
		m.handlePut(w, request)
		return
	}
	if request.Method == http.MethodPost && request.URL.Path == "/api/v1/allocate" {
		m.handlePost(w, request)
		return
	}
	http.NotFound(w, request)
}

func (m *mockAlgorithm) handlePut(w http.ResponseWriter, request *http.Request) {
	body := decodeMap(request.Body)
	nodes, _ := body["nodes"].([]any)
	byUID := make(map[string]map[string]any, len(nodes))
	for _, raw := range nodes {
		item, _ := raw.(map[string]any)
		byUID[text(item["nodeUID"])] = item
	}
	id := strings.TrimPrefix(request.URL.Path, "/internal/v1/node-static-snapshots/")
	m.mu.Lock()
	m.staticID = id
	m.static = byUID
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"accepted": true, "acceptedSnapshotId": id, "snapshotId": id,
		"algorithmBootId": m.bootID, "nodeCount": len(nodes),
	})
}

func (m *mockAlgorithm) handlePost(w http.ResponseWriter, request *http.Request) {
	body := decodeMap(request.Body)
	if err := validateRequestedAlgorithmPlan(body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_ALGORITHM_PLAN", "message": err.Error()})
		return
	}
	m.mu.Lock()
	staticID := m.staticID
	static := m.static
	m.mu.Unlock()
	response := map[string]any{
		"requestId": body["requestId"], "taskUID": body["taskUID"], "ngdUID": body["ngdUID"],
		"ngdGeneration": body["ngdGeneration"], "algorithmBootId": m.bootID,
		"nodeStaticSnapshotId": staticID, "schedulerStateSnapshotId": body["schedulerStateSnapshotId"],
		"metricSnapshotId": "mock-metrics-3000", "metricSnapshotCapturedAt": time.Now().UTC().Format(time.RFC3339Nano),
		"degraded": false, "warnings": []any{}, "status": "SUCCESS",
	}
	state, _ := body["schedulerState"].([]any)
	eligible := make([]map[string]any, 0, 32)
	for _, raw := range state {
		item, _ := raw.(map[string]any)
		if item == nil || item["ready"] != true || item["unschedulable"] == true {
			continue
		}
		uid := text(item["nodeUID"])
		node := static[uid]
		topology, _ := node["topology"].(map[string]any)
		if topology == nil || text(topology["borderSwitchId"]) != "border-01" {
			continue
		}
		eligible = append(eligible, map[string]any{"nodeUID": uid, "nodeName": text(item["nodeName"]), "score": int64(90)})
	}
	sort.Slice(eligible, func(i, j int) bool { return text(eligible[i]["nodeName"]) < text(eligible[j]["nodeName"]) })
	if len(eligible) > 32 {
		eligible = eligible[:32]
	}
	response["candidateNodeGroups"] = []any{map[string]any{
		"rank": int64(1), "groupId": "border:border-01", "topologyLevel": "borderSwitch",
		"groupScore": 90.0, "nodes": eligible,
	}}
	writeJSON(w, http.StatusOK, response)
}

// 第二组不执行真实Python算法，因此Mock必须显式检查PRC是否把完整正式NGD
// 原样发送给Algorithm，并确认不再下发可编排algorithms字段。
func validateRequestedAlgorithmPlan(body map[string]any) error {
	ngd, _ := body["ngd"].(map[string]any)
	if ngd == nil {
		return fmt.Errorf("original ngd spec is required")
	}
	topology, _ := ngd["topologyRequirement"].(map[string]any)
	if topology == nil || text(topology["widestAllowedLevel"]) != "coreSwitch" {
		return fmt.Errorf("ngd.topologyRequirement.widestAllowedLevel must be coreSwitch")
	}
	if _, exists := body["algorithms"]; exists {
		return fmt.Errorf("PRC must not send a top-level algorithms plan")
	}
	if _, exists := ngd["algorithms"]; exists {
		return fmt.Errorf("test NGD must omit optional spec.algorithms")
	}
	for _, field := range []string{"schedulerName", "nodeSelector", "topologyRequirement", "maxCandidateGroups", "maxNodes", "quota", "minResources", "minThroughput", "crossClusterAffinity", "intraClusterAffinity", "networkReachability", "preferredSubnet"} {
		if _, exists := ngd[field]; !exists {
			return fmt.Errorf("ngd.%s was not preserved", field)
		}
	}
	if intValue(ngd, "maxCandidateGroups") != 3 {
		return fmt.Errorf("ngd.maxCandidateGroups must be 3")
	}
	return nil
}

func main() {
	// fatalf uses panic so all envtest/process cleanup defers run on failures.
	// This outer recovery converts the panic back to a clean non-zero CLI exit.
	defer func() {
		if value := recover(); value != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %v\n", value)
			os.Exit(1)
		}
	}()
	projectRoot := flag.String("project-root", "", "project root")
	fixtureDir := flag.String("fixture-dir", "", "generated input fixture directory")
	outputDir := flag.String("output-dir", "", "run output directory")
	suite := flag.String("suite", "group2", "group2 or group3")
	externalAlgorithmURL := flag.String("algorithm-url", "", "real Algorithm URL used by group3")
	prometheusControlURL := flag.String("prometheus-control-url", "", "Mock Prometheus control URL used by group3 cold case")
	mode := flag.String("mode", "timing", "timing or evidence")
	flag.Parse()
	if *projectRoot == "" || *fixtureDir == "" || *outputDir == "" {
		fatalf("project-root, fixture-dir and output-dir are required")
	}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		fatalf("KUBEBUILDER_ASSETS is empty")
	}
	if *mode != "timing" && *mode != "evidence" {
		fatalf("mode must be timing or evidence")
	}
	if err := os.MkdirAll(*outputDir, 0o755); err != nil {
		fatalf("create output: %v", err)
	}

	crdDir, err := os.MkdirTemp("", "ngd-ngg-envtest-crds-")
	if err != nil {
		fatalf("create CRD temp dir: %v", err)
	}
	defer os.RemoveAll(crdDir)
	copyCRDs(*projectRoot, crdDir)

	environment := &envtest.Environment{CRDDirectoryPaths: []string{crdDir}, BinaryAssetsDirectory: os.Getenv("KUBEBUILDER_ASSETS")}
	config, err := environment.Start()
	if err != nil {
		fatalf("start envtest: %v", err)
	}
	defer func() { _ = environment.Stop() }()
	config.QPS = 1000
	config.Burst = 2000

	scheme := runtime.NewScheme()
	must(clientgoscheme.AddToScheme(scheme))
	apiClient, err := client.New(config, client.Options{Scheme: scheme})
	must(err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mock *mockAlgorithm
	var server *http.Server
	algorithmURL := *externalAlgorithmURL
	if *suite == "group2" {
		mock = &mockAlgorithm{bootID: "mock-algorithm-group2", static: map[string]map[string]any{}}
		listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
		must(listenErr)
		server = &http.Server{Handler: mock, ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = server.Serve(listener) }()
		defer server.Shutdown(context.Background())
		algorithmURL = "http://" + listener.Addr().String()
	} else if *suite == "group3" {
		if algorithmURL == "" || *prometheusControlURL == "" {
			fatalf("group3 requires algorithm-url and prometheus-control-url")
		}
	} else {
		fatalf("unknown suite %q", *suite)
	}

	mgr, err := ctrl.NewManager(config, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", LeaderElection: false})
	must(err)
	exchanges := &exchangeRecorder{root: *outputDir, identity: map[string]string{}, buffer: map[string][]controller.AlgorithmExchange{}}
	var algorithmRecorder controller.AlgorithmExchangeRecorder
	if *mode == "evidence" {
		algorithmRecorder = exchanges.record
	}
	watches := &watchTracker{starts: map[string]time.Time{}}
	reconciler := &controller.NodeGroupDemandReconciler{
		Client: mgr.GetClient(), Scheme: scheme, AlgorithmURL: algorithmURL, ClusterID: "envtest-3000",
		AlgorithmRecorder: algorithmRecorder, DebugAlgorithmTrace: false, ReconcileObserver: watches.observe,
	}
	must(reconciler.SetupWithManager(mgr))
	go func() {
		if err := mgr.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "PRC manager stopped: %v\n", err)
		}
	}()

	createFixtureObjects(ctx, apiClient, *fixtureDir)
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		fatalf("PRC cache did not sync")
	}

	baseDemand := readYAMLMap(filepath.Join(*projectRoot, "test_suites/group2_prc/input/formal-ngd.yaml"))
	if *suite == "group3" {
		summary := runGroup3(ctx, apiClient, baseDemand, *prometheusControlURL, *outputDir, exchanges, watches, *mode)
		if *mode == "evidence" {
			exchanges.flushAll()
		}
		writeJSONFile(filepath.Join(*outputDir, resultFile(*mode)), summary)
		fmt.Printf("group3 PASS output=%s\n", *outputDir)
		return
	}
	// 第二组只保留 normal_create：T0严格取PRC开始处理该NGD Watch事件的时刻。
	exchanges.setCase("normal_create")
	normal := demandFrom(baseDemand, "demand-3000-normal")
	must(apiClient.Create(ctx, normal))
	exchanges.registerIdentity(string(normal.GetUID()), "normal_create")
	t0 := watches.wait(string(normal.GetUID()), normal.GetGeneration(), 30*time.Second)
	grant, createdAt, readyAt := waitGrant(ctx, apiClient, normal.GetName(), 32, 90*time.Second)
	duration := readyAt.Sub(t0).Seconds() * 1000
	caseOutput := map[string]any{"grantGeneration": grant.GetGeneration(), "nodeCount": nestedInt(grant.Object, "status", "resolvedCapacity", "nodes")}
	result := caseResult{ID: "normal_create", Status: "PASS", Timed: *mode == "timing", Output: caseOutput}
	if *mode == "timing" {
		result.DurationMS = measured(duration)
		caseOutput["nggCreatedAfterMs"] = createdAt.Sub(t0).Seconds() * 1000
		caseOutput["nggReadyAfterMs"] = duration
	} else {
		caseRoot := filepath.Join(*outputDir, "normal_create")
		writeYAMLFile(filepath.Join(caseRoot, "input/ngd-input.yaml"), normal.Object)
		writeYAMLFile(filepath.Join(caseRoot, "output/ngg-generated.yaml"), grant.Object)
		writeYAMLFile(filepath.Join(caseRoot, "output/ngd-final.yaml"), currentDemand(ctx, apiClient, normal.GetName()))
		exchanges.flushAll()
	}
	writeJSONFile(filepath.Join(*outputDir, resultFile(*mode)), map[string]any{
		"status": "PASS", "nodeCount": 3000, "boundPodCount": 100,
		"mode":               *mode,
		"timeUnit":           "ms",
		"coreTimingBoundary": "PRC starts formal NGD Watch/Reconcile -> formal NGG spec and statuses ready",
		"excluded":           []any{"environment preparation", "protocol evidence serialization", "file writes"},
		"cases":              []caseResult{result},
	})
	fmt.Printf("group2 PASS output=%s\n", *outputDir)
}

func runGroup3(ctx context.Context, c client.Client, baseDemand map[string]any, prometheusControlURL, outputDir string, exchanges *exchangeRecorder, watches *watchTracker, mode string) map[string]any {
	results := []caseResult{}

	// Cold：Prometheus放行、Pod/NGD创建均为前置；T0取PRC收到NGD Watch。
	exchanges.setCase("cold_cache")
	postControl(prometheusControlURL + "/control/open")
	coldPods := createPendingPods(ctx, c, "cold", 20)
	coldDemand := demandFrom(baseDemand, "full-chain-cold")
	must(c.Create(ctx, coldDemand))
	exchanges.registerIdentity(string(coldDemand.GetUID()), "cold_cache")
	coldWatch := watches.wait(string(coldDemand.GetUID()), coldDemand.GetGeneration(), 30*time.Second)
	coldGrant, _, _ := waitGrant(ctx, c, coldDemand.GetName(), 0, 120*time.Second)
	coldNGGDuration := time.Since(coldWatch)
	coldDemandFinal := currentDemand(ctx, c, coldDemand.GetName())
	// 删除NGD仅用于阻止测试中的Pod Watch刷新，不计入业务耗时。
	must(c.Delete(ctx, coldDemand))
	bindingStart := time.Now()
	coldConsumer, coldEvidence := consumeAndBind(ctx, c, coldGrant.GetName(), coldPods)
	coldDuration := coldNGGDuration.Seconds()*1000 + time.Since(bindingStart).Seconds()*1000
	if intValue(coldConsumer, "outsideAuthorizedNodeCount") != 0 || intValue(coldConsumer, "bindingCount") != 20 {
		fatalf("cold consumer binding assertion failed: %v", coldConsumer)
	}
	coldResult := caseResult{ID: "cold_cache", Status: "PASS", Timed: mode == "timing", Output: coldConsumer}
	if mode == "timing" {
		coldResult.DurationMS = measured(coldDuration)
	} else {
		caseRoot := filepath.Join(outputDir, "cold_cache")
		writeYAMLFile(filepath.Join(caseRoot, "input/ngd-input.yaml"), coldDemand.Object)
		writePodsYAML(filepath.Join(caseRoot, "input/pending-pods.yaml"), coldPods)
		writeYAMLFile(filepath.Join(caseRoot, "output/ngd-final.yaml"), coldDemandFinal)
		writeConsumerEvidence(filepath.Join(caseRoot, "process/ngg-consumer"), coldEvidence)
		writeYAMLFile(filepath.Join(caseRoot, "output/ngg-generated.yaml"), currentGrant(ctx, c, coldGrant.GetName()))
		writePodsFromAPIYAML(ctx, c, filepath.Join(caseRoot, "output/pods-after-binding.yaml"), coldPods)
	}
	results = append(results, coldResult)
	deleteGrantAndPods(ctx, c, coldGrant.GetName(), coldPods)

	// Warm：同样从PRC收到NGD Watch开始，到NGG及Binding业务完成。
	exchanges.setCase("warm_cache")
	warmPods := createPendingPods(ctx, c, "warm", 20)
	warmDemand := demandFrom(baseDemand, "full-chain-warm")
	must(c.Create(ctx, warmDemand))
	exchanges.registerIdentity(string(warmDemand.GetUID()), "warm_cache")
	warmWatch := watches.wait(string(warmDemand.GetUID()), warmDemand.GetGeneration(), 30*time.Second)
	warmGrant, _, _ := waitGrant(ctx, c, warmDemand.GetName(), 0, 90*time.Second)
	warmNGGDuration := time.Since(warmWatch)
	warmDemandFinal := currentDemand(ctx, c, warmDemand.GetName())
	must(c.Delete(ctx, warmDemand))
	bindingStart = time.Now()
	warmConsumer, warmEvidence := consumeAndBind(ctx, c, warmGrant.GetName(), warmPods)
	warmDuration := warmNGGDuration.Seconds()*1000 + time.Since(bindingStart).Seconds()*1000
	if intValue(warmConsumer, "outsideAuthorizedNodeCount") != 0 || intValue(warmConsumer, "bindingCount") != 20 {
		fatalf("warm consumer binding assertion failed: %v", warmConsumer)
	}
	warmResult := caseResult{ID: "warm_cache", Status: "PASS", Timed: mode == "timing", Output: warmConsumer}
	if mode == "timing" {
		warmResult.DurationMS = measured(warmDuration)
	} else {
		caseRoot := filepath.Join(outputDir, "warm_cache")
		writeYAMLFile(filepath.Join(caseRoot, "input/ngd-input.yaml"), warmDemand.Object)
		writePodsYAML(filepath.Join(caseRoot, "input/pending-pods.yaml"), warmPods)
		writeYAMLFile(filepath.Join(caseRoot, "output/ngd-final.yaml"), warmDemandFinal)
		writeConsumerEvidence(filepath.Join(caseRoot, "process/ngg-consumer"), warmEvidence)
		writeYAMLFile(filepath.Join(caseRoot, "output/ngg-generated.yaml"), currentGrant(ctx, c, warmGrant.GetName()))
		writePodsFromAPIYAML(ctx, c, filepath.Join(caseRoot, "output/pods-after-binding.yaml"), warmPods)
	}
	results = append(results, warmResult)
	deleteGrantAndPods(ctx, c, warmGrant.GetName(), warmPods)

	return map[string]any{
		"status": "PASS", "nodeCount": int64(3000), "boundPodFixtureCount": int64(100),
		"mode": mode, "timeUnit": "ms", "timingBoundary": "PRC starts formal NGD Watch/Reconcile -> NGG ready plus Binding business duration",
		"excluded":  []any{"environment preparation", "test-only NGD deletion gap", "evidence serialization", "file writes"},
		"testClaim": "full-chain simulated; not real scheduler performance", "cases": results,
	}
}

func createPendingPods(ctx context.Context, c client.Client, prefix string, count int) []*corev1.Pod {
	result := make([]*corev1.Pod, 0, count)
	for index := 0; index < count; index++ {
		pod := &corev1.Pod{}
		pod.Namespace = "ngd-ngg-test"
		pod.Name = fmt.Sprintf("managed-%s-%02d", prefix, index)
		pod.Spec.SchedulerName = "volcano"
		pod.Spec.Containers = []corev1.Container{{Name: "work", Image: "fixture.invalid/work:never-run"}}
		must(c.Create(ctx, pod))
		result = append(result, pod)
	}
	return result
}

type consumerEvidence struct {
	GrantInput map[string]any
	Authorized map[string]any
	Bindings   map[string]any
	Status     any
}

func consumeAndBind(ctx context.Context, c client.Client, grantName string, pods []*corev1.Pod) (map[string]any, consumerEvidence) {
	grant := &unstructured.Unstructured{}
	grant.SetGroupVersionKind(formalGrantGVK)
	must(c.Get(ctx, client.ObjectKey{Name: grantName}, grant))
	phase := nestedString(grant.Object, "status", "phase")
	grantInput := deepCopy(grant.Object)
	parsed, parseErr := formalgrant.Parse(grant, "volcano", time.Now(), 2*time.Minute)
	authorized := []string{}
	if parseErr == nil {
		for _, node := range formalgrant.Merge([]formalgrant.Grant{parsed}) {
			authorized = append(authorized, node.Name)
		}
	}
	bindings := 0
	outside := 0
	allowed := map[string]struct{}{}
	for _, name := range authorized {
		allowed[name] = struct{}{}
	}
	authorizedEvidence := map[string]any{"phase": phase, "nodeCount": len(authorized), "nodes": authorized, "parseError": errorText(parseErr)}
	bindingEvidence := []any{}
	if len(authorized) > 0 {
		for index, original := range pods {
			pod := &corev1.Pod{}
			must(c.Get(ctx, client.ObjectKey{Namespace: original.Namespace, Name: original.Name}, pod))
			target := authorized[index%len(authorized)]
			binding := &corev1.Binding{
				ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
				Target:     corev1.ObjectReference{APIVersion: "v1", Kind: "Node", Name: target},
			}
			must(c.SubResource("binding").Create(ctx, pod, binding))
			must(c.Get(ctx, client.ObjectKey{Namespace: original.Namespace, Name: original.Name}, pod))
			bindings++
			if pod.Spec.NodeName != target {
				fatalf("binding API did not persist target for Pod %s", pod.Name)
			}
			if _, ok := allowed[target]; !ok {
				outside++
			}
			bindingEvidence = append(bindingEvidence, map[string]any{"pod": pod.Namespace + "/" + pod.Name, "requestedTarget": target, "boundNodeName": pod.Spec.NodeName, "authorized": true})
		}
		latest := &unstructured.Unstructured{}
		latest.SetGroupVersionKind(formalGrantGVK)
		must(c.Get(ctx, client.ObjectKey{Name: grantName}, latest))
		status, _, _ := unstructured.NestedMap(latest.Object, "status")
		status["consumer"] = map[string]any{"acceptedGeneration": latest.GetGeneration(), "used": map[string]any{"cpu": "0", "memory": "0"}}
		_ = unstructured.SetNestedMap(latest.Object, status, "status")
		must(c.Status().Update(ctx, latest))
		grant = latest
	}
	summary := map[string]any{
		"grantPhase": phase, "authorizedNodeCount": int64(len(authorized)),
		"bindingCount": int64(bindings), "outsideAuthorizedNodeCount": int64(outside),
		"acceptedGeneration": grant.GetGeneration(),
	}
	evidence := consumerEvidence{
		GrantInput: grantInput, Authorized: authorizedEvidence,
		Bindings: map[string]any{"inputPods": podNames(pods), "bindingCount": bindings, "bindings": bindingEvidence},
		Status:   grant.Object["status"],
	}
	return summary, evidence
}

func writeConsumerEvidence(directory string, evidence consumerEvidence) {
	writeJSONFile(filepath.Join(directory, "01-input-formal-ngg.json"), evidence.GrantInput)
	writeJSONFile(filepath.Join(directory, "02-output-authorized-nodes.json"), evidence.Authorized)
	writeJSONFile(filepath.Join(directory, "03-input-output-bindings.json"), evidence.Bindings)
	writeJSONFile(filepath.Join(directory, "04-output-consumer-status.json"), evidence.Status)
}

func deleteGrantAndPods(ctx context.Context, c client.Client, grantName string, pods []*corev1.Pod) {
	grant := &unstructured.Unstructured{}
	grant.SetGroupVersionKind(formalGrantGVK)
	grant.SetName(grantName)
	_ = c.Delete(ctx, grant)
	for _, pod := range pods {
		_ = c.Delete(ctx, pod)
	}
}

func postControl(endpoint string) {
	request, err := http.NewRequest(http.MethodPost, endpoint, nil)
	must(err)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	must(err)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fatalf("Prometheus control returned HTTP %d", response.StatusCode)
	}
}

func intValue(object map[string]any, key string) int64 {
	value := object[key]
	switch number := value.(type) {
	case int64:
		return number
	case float64:
		return int64(number)
	case json.Number:
		result, _ := number.Int64()
		return result
	default:
		return 0
	}
}

func createFixtureObjects(ctx context.Context, c client.Client, fixtureDir string) {
	namespace := &corev1.Namespace{}
	namespace.Name = "ngd-ngg-test"
	must(c.Create(ctx, namespace))
	var nodes fixtureFile
	readJSON(filepath.Join(fixtureDir, "kubernetes-nodes.json"), &nodes)
	for _, raw := range nodes.Items {
		var node corev1.Node
		must(json.Unmarshal(raw, &node))
		node.UID = ""
		desiredStatus := node.Status.DeepCopy()
		node.Status = corev1.NodeStatus{}
		must(c.Create(ctx, &node))
		created := &corev1.Node{}
		must(c.Get(ctx, client.ObjectKey{Name: node.Name}, created))
		created.Status = *desiredStatus
		must(c.Status().Update(ctx, created))
	}
	var pods fixtureFile
	readJSON(filepath.Join(fixtureDir, "kubernetes-pods.json"), &pods)
	for _, raw := range pods.Items {
		var pod corev1.Pod
		must(json.Unmarshal(raw, &pod))
		desiredStatus := pod.Status.DeepCopy()
		pod.Status = corev1.PodStatus{}
		must(c.Create(ctx, &pod))
		created := &corev1.Pod{}
		must(c.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}, created))
		created.Status = *desiredStatus
		must(c.Status().Update(ctx, created))
	}
}

func demandFrom(base map[string]any, name string) *unstructured.Unstructured {
	copy := deepCopy(base)
	object := &unstructured.Unstructured{Object: copy}
	object.SetGroupVersionKind(formalDemandGVK)
	object.SetName(name)
	object.SetResourceVersion("")
	object.SetUID("")
	return object
}

func waitGrant(ctx context.Context, c client.Client, demandName string, expectedNodes int64, timeout time.Duration) (*unstructured.Unstructured, time.Time, time.Time) {
	deadline := time.Now().Add(timeout)
	createdAt := time.Time{}
	name := "ngg-" + demandName
	for time.Now().Before(deadline) {
		grant := &unstructured.Unstructured{}
		grant.SetGroupVersionKind(formalGrantGVK)
		err := c.Get(ctx, client.ObjectKey{Name: name}, grant)
		if err == nil {
			if createdAt.IsZero() {
				createdAt = time.Now()
			}
			nodes, _, _ := unstructured.NestedSlice(grant.Object, "spec", "nodes")
			phase := nestedString(grant.Object, "status", "phase")
			resolved := nestedInt(grant.Object, "status", "resolvedCapacity", "nodes")
			countMatches := (expectedNodes == 0 && len(nodes) > 0) || int64(len(nodes)) == expectedNodes
			if countMatches && phase == "Active" && resolved == int64(len(nodes)) {
				demand := waitDemandPhase(ctx, c, demandName, "Fulfilled", 5*time.Second)
				if nestedString(demand.Object, "status", "grantRef") == name {
					return grant, createdAt, time.Now()
				}
			}
		} else if !apierrors.IsNotFound(err) {
			fatalf("get NGG: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	fatalf("timeout waiting for ready NGG %s", name)
	return nil, time.Time{}, time.Time{}
}

func waitDemandPhase(ctx context.Context, c client.Client, name, phase string, timeout time.Duration) *unstructured.Unstructured {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		demand := &unstructured.Unstructured{}
		demand.SetGroupVersionKind(formalDemandGVK)
		if err := c.Get(ctx, client.ObjectKey{Name: name}, demand); err == nil && nestedString(demand.Object, "status", "phase") == phase {
			return demand
		}
		time.Sleep(50 * time.Millisecond)
	}
	fatalf("timeout waiting for NGD %s phase %s", name, phase)
	return nil
}

func copyCRDs(root, destination string) {
	paths := []string{
		"config/crd/nodegroupdemand.yaml", "config/crd/nodenetworktopology.yaml",
		"config/crd/nodegroupgrant-platform.yaml",
		"docs/paas-schedbridge-master/crd-deploy/nodegroupdemand-crd.yaml",
	}
	for index, relative := range paths {
		raw, err := os.ReadFile(filepath.Join(root, relative))
		must(err)
		must(os.WriteFile(filepath.Join(destination, fmt.Sprintf("%02d.yaml", index)), raw, 0o644))
	}
}

func decodeMap(reader io.Reader) map[string]any {
	defer func() { _ = reader.(io.ReadCloser).Close() }()
	decoder := json.NewDecoder(io.LimitReader(reader, 32<<20))
	decoder.UseNumber()
	value := map[string]any{}
	must(decoder.Decode(&value))
	return value
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONFile(path string, value any) {
	must(os.MkdirAll(filepath.Dir(path), 0o755))
	raw, err := json.MarshalIndent(value, "", "  ")
	must(err)
	must(os.WriteFile(path, append(raw, '\n'), 0o644))
}

func appendJSONLine(path string, value any) {
	must(os.MkdirAll(filepath.Dir(path), 0o755))
	raw, err := json.Marshal(value)
	must(err)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	must(err)
	defer file.Close()
	_, err = file.Write(append(raw, '\n'))
	must(err)
}

func writeYAMLFile(path string, value any) {
	must(os.MkdirAll(filepath.Dir(path), 0o755))
	raw, err := yaml.Marshal(value)
	must(err)
	must(os.WriteFile(path, raw, 0o644))
}

func resultFile(mode string) string {
	if mode == "evidence" {
		return "evidence-result.json"
	}
	return "timing-result.json"
}

func writeRawJSON(path string, raw []byte) {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		value = map[string]any{"raw": string(raw)}
	}
	writeJSONFile(path, value)
}

func currentDemand(ctx context.Context, c client.Client, name string) map[string]any {
	demand := &unstructured.Unstructured{}
	demand.SetGroupVersionKind(formalDemandGVK)
	must(c.Get(ctx, client.ObjectKey{Name: name}, demand))
	return deepCopy(demand.Object)
}

func currentGrant(ctx context.Context, c client.Client, name string) map[string]any {
	grant := &unstructured.Unstructured{}
	grant.SetGroupVersionKind(formalGrantGVK)
	must(c.Get(ctx, client.ObjectKey{Name: name}, grant))
	return deepCopy(grant.Object)
}

func writePodsYAML(path string, pods []*corev1.Pod) {
	items := make([]any, 0, len(pods))
	for _, pod := range pods {
		items = append(items, pod)
	}
	writeYAMLFile(path, map[string]any{"apiVersion": "v1", "kind": "PodList", "items": items})
}

func writePodsFromAPIYAML(ctx context.Context, c client.Client, path string, pods []*corev1.Pod) {
	items := make([]any, 0, len(pods))
	for _, original := range pods {
		pod := &corev1.Pod{}
		must(c.Get(ctx, client.ObjectKey{Namespace: original.Namespace, Name: original.Name}, pod))
		items = append(items, pod)
	}
	writeYAMLFile(path, map[string]any{"apiVersion": "v1", "kind": "PodList", "items": items})
}

func podNames(pods []*corev1.Pod) []string {
	result := make([]string, 0, len(pods))
	for _, pod := range pods {
		result = append(result, pod.Namespace+"/"+pod.Name)
	}
	return result
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func readJSON(path string, target any) {
	raw, err := os.ReadFile(path)
	must(err)
	must(json.Unmarshal(raw, target))
}
func readYAMLMap(path string) map[string]any {
	raw, err := os.ReadFile(path)
	must(err)
	value := map[string]any{}
	must(yaml.Unmarshal(raw, &value))
	return value
}
func deepCopy(value map[string]any) map[string]any {
	raw, _ := json.Marshal(value)
	result := map[string]any{}
	_ = json.Unmarshal(raw, &result)
	return result
}
func text(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
func nestedString(object map[string]any, fields ...string) string {
	value, _, _ := unstructured.NestedString(object, fields...)
	return value
}
func nestedInt(object map[string]any, fields ...string) int64 {
	value, _, _ := unstructured.NestedInt64(object, fields...)
	return value
}
func must(err error) {
	if err != nil {
		fatalf("%v", err)
	}
}
func fatalf(format string, values ...any) {
	panic(fmt.Sprintf(format, values...))
}
