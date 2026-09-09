package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	grantGroup            = "scheduling.platform.example.io"
	version               = "v1alpha1"
	demandKind            = "NodeGroupDemand"
	grantKind             = "NodeGroupGrant"
	defaultRetry          = 10 * time.Second
	prcSpecFieldManager   = "ngd-ngg-prc-spec"
	prcStatusFieldManager = "ngd-ngg-prc-status"
)

var (
	platformDemandGVK  = schema.GroupVersionKind{Group: grantGroup, Version: version, Kind: demandKind}
	platformDemandList = schema.GroupVersionKind{
		Group: grantGroup, Version: version, Kind: demandKind + "List",
	}
	grantGVK = schema.GroupVersionKind{Group: grantGroup, Version: version, Kind: grantKind}
)

// DemandProcessor executes one complete NGD business calculation. It is shared
// by the initial event path and every scheduled refresh so both paths publish
// NGG and status with identical semantics.
type DemandProcessor struct {
	client.Client
	AlgorithmURL string
	HTTPClient   *http.Client
	// StaticSnapshots由独立NodeStaticSnapshotReconciler维护；任务Reconcile只读取已确认身份。
	StaticSnapshots *StaticSnapshotState
	// AlgorithmRecorder/DebugAlgorithmTrace 仅用于显式开启的协议留痕；
	// 默认值不会改变生产请求、响应或调度结果。
	AlgorithmRecorder   AlgorithmExchangeRecorder
	DebugAlgorithmTrace bool
	// ReconcileObserver 在正式NGD开始业务处理时通知测试计时器；生产为nil。
	ReconcileObserver func(uid string, generation int64, observedAt time.Time)
}

// Process executes one refresh. RequeueAfter is returned as a scheduling hint
// to RefreshScheduler and is never returned to the NGD event controller.
func (r *DemandProcessor) Process(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	demand := newUnstructured(platformDemandGVK)
	if err := r.Get(ctx, request.NamespacedName, demand); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.reconcilePlatformDemand(ctx, demand)
}

// reconcilePlatformDemand handles the formal, cluster-scoped China Unicom
// resource-pool contract. The NGD UID itself is the request identity.
func (r *DemandProcessor) reconcilePlatformDemand(ctx context.Context, demand *unstructured.Unstructured) (ctrl.Result, error) {
	started := time.Now()
	log := ctrl.LoggerFrom(ctx).WithValues(
		"formalNGD", demand.GetName(), "ngdUID", demand.GetUID(), "generation", demand.GetGeneration(),
	)
	log.Info("starting NGD calculation")
	if r.ReconcileObserver != nil {
		r.ReconcileObserver(string(demand.GetUID()), demand.GetGeneration(), time.Now())
	}
	spec, _, _ := unstructured.NestedMap(demand.Object, "spec")
	if err := validatePlatformDemandSpec(spec); err != nil {
		return r.failPlatformDemand(ctx, demand, "UnsupportedDemand", err.Error(), false)
	}

	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Nodes: %w", err)
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Pods: %w", err)
	}
	log.Info("captured Kubernetes scheduling inputs", "nodeCount", len(nodes.Items), "podCount", len(pods.Items))
	staticStatus, ready := r.StaticSnapshots.Current()
	if !ready {
		return r.failPlatformDemand(ctx, demand, "StaticSnapshotNotReady", "waiting for independent Node static snapshot synchronization", true)
	}
	staticID := staticStatus.SnapshotID
	stateID, stateCapturedAt, schedulerState, err := buildSchedulerState(nodes.Items, pods.Items)
	if err != nil {
		return ctrl.Result{}, err
	}
	log.Info("prepared Algorithm snapshots",
		"nodeStaticSnapshotId", shortHash(staticID), "staticNodeCount", staticStatus.NodeCount,
		"schedulerStateSnapshotId", shortHash(stateID), "schedulerNodeCount", len(schedulerState),
	)

	algorithm := AlgorithmClient{BaseURL: strings.TrimRight(r.AlgorithmURL, "/"), Client: r.httpClient(), Recorder: r.AlgorithmRecorder}
	grant := newUnstructured(grantGVK)
	grantKey := types.NamespacedName{Name: grantName(demand.GetName())}
	grantErr := r.Get(ctx, grantKey, grant)
	if grantErr != nil && !apierrors.IsNotFound(grantErr) {
		return ctrl.Result{}, grantErr
	}
	var existing *unstructured.Unstructured
	if grantErr == nil {
		existing = grant
	}

	requestID := fmt.Sprintf("%s-generation-%d-state-%s", demand.GetUID(), demand.GetGeneration(), shortHash(stateID))
	// 联通 NGD spec 作为一个完整对象原样传给 Algorithm。Algorithm读取
	// nodeSelector、topologyLabels、maxNodes、quota和minResources；具体拓扑图、
	// 物理交换机到逻辑域的映射及固定算法顺序均由Algorithm Server管理。
	ngdSpec, _ := runtime.DeepCopyJSONValue(spec).(map[string]any)
	algorithmRequest := map[string]any{
		"requestId": requestID, "taskUID": string(demand.GetUID()), "ngdUID": string(demand.GetUID()),
		"ngdGeneration": demand.GetGeneration(), "requestMode": "resourcePool",
		"nodeStaticSnapshotId": staticID, "schedulerStateSnapshotId": stateID,
		"schedulerStateCapturedAt": stateCapturedAt, "schedulerState": schedulerState,
		"ngd": ngdSpec,
	}
	if r.DebugAlgorithmTrace {
		algorithmRequest["debugTrace"] = true
	}
	timeout := algorithmTimeout(spec)
	algorithmStarted := time.Now()
	log.Info("calling Algorithm Server", "requestId", requestID, "timeout", timeout)
	response, err := algorithm.calculate(ctx, algorithmRequest, timeout)
	if err != nil {
		log.Error(err, "Algorithm Server request failed", "requestId", requestID, "elapsedMs", elapsedMilliseconds(algorithmStarted))
		return r.failPlatformDemand(ctx, demand, "AlgorithmRequestFailed", err.Error(), true)
	}
	log.Info("received Algorithm Server result",
		"requestId", requestID, "status", response.Status, "candidateGroupCount", len(response.CandidateNodeGroups),
		"degraded", response.Degraded, "warningCount", len(response.Warnings), "elapsedMs", elapsedMilliseconds(algorithmStarted),
	)
	current, err := r.demandStillCurrent(ctx, demand)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !current {
		log.Info("discarded stale Algorithm result", "uid", demand.GetUID(), "generation", demand.GetGeneration())
		return ctrl.Result{}, nil
	}
	if err := validateResponse(response, requestID, demand, demand.GetUID(), staticID, stateID, staticStatus.AlgorithmBootID, schedulerState); err != nil {
		return r.failPlatformDemand(ctx, demand, "AlgorithmResponseInvalid", err.Error(), true)
	}
	if len(response.CandidateNodeGroups) == 0 {
		if existing != nil {
			if err := r.setPlatformGrantReturned(ctx, existing); err != nil {
				return ctrl.Result{}, err
			}
		}
		return r.failPlatformDemand(ctx, demand, "NoFeasibleGroup", "Algorithm returned no feasible topology group", false)
	}

	selected := response.CandidateNodeGroups[0]
	log.Info("selected highest-ranked candidate group",
		"requestId", requestID, "groupId", selected.GroupID, "rank", selected.Rank,
		"groupScore", selected.GroupScore, "nodeCount", len(selected.Nodes),
	)
	now := time.Now().UTC()
	desiredSpec := buildPlatformGrantSpec(demand, spec, response, now)
	log.Info("applying NGG spec", "ngg", grantName(demand.GetName()))
	applied, err := r.upsertGrant(ctx, demand, desiredSpec)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.setPlatformGrantStatus(ctx, applied, selected, nodes.Items); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("updated NGG status", "ngg", applied.GetName(), "phase", "Active", "nodeCount", len(selected.Nodes))
	message := fmt.Sprintf("selectedGroup=%s nodes=%d", selected.GroupID, len(selected.Nodes))
	if err := r.setPlatformDemandStatus(ctx, demand, "Fulfilled", applied.GetName(), int64(len(selected.Nodes)), message); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("published formal platform NGG",
		"ngg", applied.GetName(), "selectedGroup", selected.GroupID, "nodeCount", len(selected.Nodes),
		"ngdPhase", "Fulfilled", "elapsedMs", elapsedMilliseconds(started),
	)
	return ctrl.Result{}, nil
}

func validatePlatformDemandSpec(spec map[string]any) error {
	if stringValue(spec, "schedulerName") == "" {
		return fmt.Errorf("spec.schedulerName is required")
	}
	// minThroughput、IP亲和、网络可达性和preferredSubnet均按联通原始契约
	// 原样透传；当前阶段处理资源需求、nodeSelector和五级topologyLabels。
	for _, field := range []string{"minResources", "quota"} {
		resources := mapOrEmpty(spec, field)
		for _, name := range []string{"cpu", "memory"} {
			raw := stringValue(resources, name)
			if raw == "" {
				continue
			}
			if _, err := resource.ParseQuantity(raw); err != nil {
				return fmt.Errorf("spec.%s.%s is invalid: %w", field, name, err)
			}
		}
	}
	if raw, exists := spec["topologyLabels"]; exists && raw != nil {
		labels, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("spec.topologyLabels must be an object")
		}
		supported := map[string]struct{}{
			"topology.kubernetes.io/data-center":   {},
			"topology.kubernetes.io/room":          {},
			"topology.kubernetes.io/border-switch": {},
			"topology.kubernetes.io/spine-switch":  {},
			"topology.kubernetes.io/leaf-switch":   {},
		}
		for key, value := range labels {
			if _, ok := supported[key]; !ok {
				return fmt.Errorf("spec.topologyLabels key %q is unsupported", key)
			}
			if item, ok := value.(string); !ok || strings.TrimSpace(item) == "" {
				return fmt.Errorf("spec.topologyLabels[%q] must be a non-empty string", key)
			}
		}
	}
	return nil
}

func (r *DemandProcessor) demandStillCurrent(ctx context.Context, demand *unstructured.Unstructured) (bool, error) {
	latest := newUnstructured(demand.GroupVersionKind())
	if err := r.Get(ctx, client.ObjectKeyFromObject(demand), latest); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("re-read NGD before publishing result: %w", err)
	}
	return latest.GetUID() == demand.GetUID() && latest.GetGeneration() == demand.GetGeneration(), nil
}

func (r *DemandProcessor) setPlatformDemandStatus(ctx context.Context, demand *unstructured.Unstructured, phase, grantRef string, count int64, message string) error {
	status := map[string]any{
		"phase": phase, "grantRef": grantRef, "resolvedNodeCount": count,
		"lastUpdated": time.Now().UTC().Format(time.RFC3339Nano), "message": message,
	}
	return applyStatus(ctx, r.Client, demand, status)
}

func (r *DemandProcessor) failPlatformDemand(ctx context.Context, demand *unstructured.Unstructured, reason, message string, retry bool) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues(
		"formalNGD", demand.GetName(), "ngdUID", demand.GetUID(), "generation", demand.GetGeneration(),
	)
	if err := r.setPlatformDemandStatus(ctx, demand, "Failed", "", 0, reason+": "+message); err != nil {
		log.Error(err, "failed to update NGD failure status", "reason", reason)
		return ctrl.Result{}, err
	}
	log.Info("marked NGD calculation failed", "reason", reason, "retry", retry, "detail", message)
	if retry {
		return ctrl.Result{RequeueAfter: defaultRetry}, nil
	}
	return ctrl.Result{}, nil
}

func algorithmTimeout(spec map[string]any) time.Duration {
	policy := mapValue(spec, "grantPolicy")
	seconds, _, _ := unstructured.NestedInt64(policy, "algorithmTimeoutSeconds")
	if seconds <= 0 {
		seconds = 5
	}
	return time.Duration(seconds) * time.Second
}

func (r *DemandProcessor) upsertGrant(ctx context.Context, demand *unstructured.Unstructured, spec map[string]any) (*unstructured.Unstructured, error) {
	demandRef := demand.GetName()
	grantSourceName := demand.GetName()
	grant := newUnstructured(grantGVK)
	grant.SetName(grantName(grantSourceName))
	grant.SetLabels(map[string]string{
		grantGroup + "/demand-uid": shortHash(string(demand.GetUID())),
		grantGroup + "/scheduler":  stringValue(spec, "schedulerName"),
	})
	grant.SetAnnotations(map[string]string{grantGroup + "/demand-ref": demandRef})
	controller := true
	grant.SetOwnerReferences([]metav1.OwnerReference{{

		APIVersion: demand.GetAPIVersion(), Kind: demand.GetKind(), Name: demand.GetName(), UID: demand.GetUID(),
		Controller: &controller,
	}})
	_ = unstructured.SetNestedMap(grant.Object, spec, "spec")
	if err := r.Apply(ctx, client.ApplyConfigurationFromUnstructured(grant), client.FieldOwner(prcSpecFieldManager)); err != nil {
		return nil, fmt.Errorf("apply NGG spec: %w", err)
	}
	applied := newUnstructured(grantGVK)
	if err := r.Get(ctx, client.ObjectKeyFromObject(grant), applied); err != nil {
		return nil, err
	}
	return applied, nil
}

// buildPlatformGrantSpec 将 Algorithm 排名第一的拓扑组转换为联通正式 NGG 契约。
// PRC 不重新排序、不修改正常模式分数；降级/关闭负载感知时按契约使用中性分 50。
func buildPlatformGrantSpec(demand *unstructured.Unstructured, demandSpec map[string]any, response AlgorithmResponse, now time.Time) map[string]any {
	selected := response.CandidateNodeGroups[0]
	source := "normal"
	if response.MetricSnapshotID == "metrics-disabled" {
		source = "disabled"
	} else if response.MetricSnapshotID == "" || response.Degraded {
		source = "degraded"
	}
	items := make([]any, 0, len(selected.Nodes))
	for _, candidate := range selected.Nodes {
		score := candidate.Score
		if source != "normal" {
			score = 50
		}
		item := map[string]any{"name": candidate.NodeName, "score": score}
		if len(candidate.Resources) > 0 {
			resources := make(map[string]any, len(candidate.Resources))
			for name, value := range candidate.Resources {
				resources[name] = value
			}
			item["resources"] = resources
		}
		if topology := platformCandidateTopology(candidate.Topology); len(topology) > 0 {
			item["topology"] = topology
		}
		items = append(items, item)
	}
	timestamp := response.MetricSnapshotCapturedAt
	if timestamp == "" {
		timestamp = now.Format(time.RFC3339Nano)
	}
	return map[string]any{
		"schedulerName": stringValue(demandSpec, "schedulerName"),
		"version":       "v1", "timestamp": timestamp, "source": source,
		"demandRef": demand.GetName(),
		"nodes":     items,
	}
}

func platformCandidateTopology(topology map[string]any) map[string]any {
	value := map[string]any{}
	fields := map[string]string{
		"dataCenter":   "dataCenterId",
		"room":         "roomId",
		"borderSwitch": "borderDomainId",
		"spineSwitch":  "spineDomainId",
		"leafSwitch":   "leafDomainId",
	}
	for target, source := range fields {
		if item := stringValue(topology, source); item != "" {
			value[target] = item
		}
	}
	return value
}

func (r *DemandProcessor) setPlatformGrantStatus(ctx context.Context, grant *unstructured.Unstructured, selected CandidateGroup, liveNodes []corev1.Node) error {
	allowed := map[string]struct{}{}
	for _, node := range selected.Nodes {
		allowed[node.NodeName] = struct{}{}
	}
	var cpu, memory resource.Quantity
	byName := make(map[string]*corev1.Node, len(liveNodes))
	for i := range liveNodes {
		byName[liveNodes[i].Name] = &liveNodes[i]
	}
	for _, candidate := range selected.Nodes {
		cpuValue, cpuOK := candidate.Resources["cpuAvailable"]
		memoryValue, memoryOK := candidate.Resources["memoryAvailable"]
		if cpuOK && memoryOK {
			if parsed, err := resource.ParseQuantity(cpuValue); err == nil {
				cpu.Add(parsed)
			}
			if parsed, err := resource.ParseQuantity(memoryValue); err == nil {
				memory.Add(parsed)
			}
			continue
		}
		if node := byName[candidate.NodeName]; node != nil {
			cpu.Add(node.Status.Allocatable[corev1.ResourceCPU])
			memory.Add(node.Status.Allocatable[corev1.ResourceMemory])
		}
	}
	status := map[string]any{
		"phase":            "Active",
		"resolvedCapacity": map[string]any{"nodes": int64(len(allowed)), "cpu": cpu.String(), "memory": memory.String()},
	}
	// status.consumer由消费方的独立field manager维护；PRC的Apply对象不携带该字段。
	return applyStatus(ctx, r.Client, grant, status)
}

func (r *DemandProcessor) setPlatformGrantReturned(ctx context.Context, grant *unstructured.Unstructured) error {
	status := map[string]any{
		"phase":            "Returned",
		"resolvedCapacity": map[string]any{"nodes": int64(0), "cpu": "0", "memory": "0"},
	}
	return applyStatus(ctx, r.Client, grant, status)
}

func validateResponse(response AlgorithmResponse, requestID string, demand *unstructured.Unstructured, taskUID types.UID, staticID, stateID, bootID string, state []schedulerNodeState) error {
	if response.RequestID != requestID || response.TaskUID != string(taskUID) || response.NGDUID != string(demand.GetUID()) || response.NGDGeneration != demand.GetGeneration() {
		return fmt.Errorf("Algorithm response identity mismatch")
	}
	if response.NodeStaticSnapshotID != staticID || response.SchedulerStateSnapshotID != stateID || response.AlgorithmBootID != bootID {
		return fmt.Errorf("Algorithm response snapshot identity mismatch")
	}
	if len(response.CandidateNodeGroups) > 3 {
		return fmt.Errorf("Algorithm returned more than 3 groups")
	}
	if response.Status != "SUCCESS" && response.Status != "UNSATISFIABLE" {
		return fmt.Errorf("unsupported Algorithm status %q", response.Status)
	}
	if response.Status == "SUCCESS" && len(response.CandidateNodeGroups) == 0 {
		return fmt.Errorf("SUCCESS contains no candidate group")
	}
	known := map[string]string{}
	for _, item := range state {
		known[item.NodeUID] = item.NodeName
	}
	seen := map[string]struct{}{}
	for index, group := range response.CandidateNodeGroups {
		if group.GroupID == "" || len(group.Nodes) == 0 {
			return fmt.Errorf("candidate group must contain groupId and Nodes")
		}
		if group.Rank != int64(index+1) {
			return fmt.Errorf("candidate ranks are not continuous")
		}
		if index > 0 {
			previous := response.CandidateNodeGroups[index-1]
			if group.GroupScore > previous.GroupScore || (group.GroupScore == previous.GroupScore && group.GroupID < previous.GroupID) {
				return fmt.Errorf("candidate groups are not stably sorted")
			}
		}
		for _, node := range group.Nodes {
			if node.Score < 0 || node.Score > 100 {
				return fmt.Errorf("Algorithm returned Node score outside 0..100")
			}
			knownName, ok := known[node.NodeUID]
			if !ok {
				return fmt.Errorf("Algorithm returned unknown Node UID %s", node.NodeUID)
			}
			if knownName != "" && node.NodeName != knownName {
				return fmt.Errorf("Algorithm returned mismatched Node name/UID %s/%s", node.NodeName, node.NodeUID)
			}
			if _, duplicate := seen[node.NodeUID]; duplicate {
				return fmt.Errorf("candidate groups overlap on Node UID %s", node.NodeUID)
			}
			seen[node.NodeUID] = struct{}{}
		}
		for nodeIndex := 1; nodeIndex < len(group.Nodes); nodeIndex++ {
			previousNode := group.Nodes[nodeIndex-1]
			currentNode := group.Nodes[nodeIndex]
			if currentNode.Score > previousNode.Score || (currentNode.Score == previousNode.Score && currentNode.NodeName < previousNode.NodeName) {
				return fmt.Errorf("candidate Nodes are not stably sorted")
			}
		}
	}
	return nil
}

// applyStatus sends only PRC-owned fields to the status subresource. In
// particular status.consumer is intentionally absent so its consumer field
// manager remains isolated from PRC refreshes.
func applyStatus(ctx context.Context, c client.Client, object *unstructured.Unstructured, status map[string]any) error {
	applyObject := newUnstructured(object.GroupVersionKind())
	applyObject.SetName(object.GetName())
	applyObject.SetNamespace(object.GetNamespace())
	if err := unstructured.SetNestedMap(applyObject.Object, status, "status"); err != nil {
		return err
	}
	return c.Status().Apply(
		ctx,
		client.ApplyConfigurationFromUnstructured(applyObject),
		client.FieldOwner(prcStatusFieldManager),
	)
}

func (r *DemandProcessor) httpClient() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func newUnstructured(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(gvk)
	return object
}

func grantName(name string) string {
	result := "ngg-" + name
	if len(result) > 253 {
		result = result[:253]
	}
	return strings.TrimRight(result, "-")
}

func shortHash(value string) string {
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func elapsedMilliseconds(started time.Time) float64 {
	return float64(time.Since(started).Microseconds()) / 1000
}

func mapValue(object map[string]any, key string) map[string]any {
	value, _ := object[key].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func mapOrEmpty(object map[string]any, key string) map[string]any { return mapValue(object, key) }

func stringValue(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}
