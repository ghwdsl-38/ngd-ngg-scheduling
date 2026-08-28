package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	demandGroup           = "scheduling.demo.ngg.io"
	grantGroup            = "scheduling.platform.example.io"
	version               = "v1alpha1"
	demandKind            = "NodeGroupDemand"
	grantKind             = "NodeGroupGrant"
	topologyKind          = "NodeNetworkTopology"
	demandAnnotation      = "scheduling.demo.ngg.io/node-group-demand"
	defaultRetry          = 10 * time.Second
	defaultTaskPoll       = 5 * time.Second
	defaultPodPoll        = 2 * time.Second
	maxAlgorithmBodyBytes = 4 << 20
)

var (
	demandGVK          = schema.GroupVersionKind{Group: demandGroup, Version: version, Kind: demandKind}
	demandList         = schema.GroupVersionKind{Group: demandGroup, Version: version, Kind: demandKind + "List"}
	platformDemandGVK  = schema.GroupVersionKind{Group: grantGroup, Version: version, Kind: demandKind}
	platformDemandList = schema.GroupVersionKind{
		Group: grantGroup, Version: version, Kind: demandKind + "List",
	}
	grantGVK     = schema.GroupVersionKind{Group: grantGroup, Version: version, Kind: grantKind}
	topologyGVK  = schema.GroupVersionKind{Group: demandGroup, Version: version, Kind: topologyKind}
	topologyList = schema.GroupVersionKind{
		Group: demandGroup, Version: version, Kind: topologyKind + "List",
	}
)

type NodeGroupDemandReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	AlgorithmURL string
	ClusterID    string
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

// SetupWithManager uses the Kubebuilder/controller-runtime builder pattern.
// NGD generation changes are primary events; Node, Pod and NNT changes enqueue
// all demands so Pending/Unsatisfied/Degraded workloads are actively retried.
func (r *NodeGroupDemandReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.StaticSnapshots == nil {
		return fmt.Errorf("StaticSnapshotState is required")
	}
	formalDemand := newUnstructured(platformDemandGVK)
	legacyDemand := newUnstructured(demandGVK)
	topology := newUnstructured(topologyGVK)
	return ctrl.NewControllerManagedBy(mgr).
		For(formalDemand, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(legacyDemand, &handler.EnqueueRequestForObject{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.mapAllDemands)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.mapAllDemands)).
		Watches(topology, handler.EnqueueRequestsFromMapFunc(r.mapAllDemands)).
		Complete(r)
}

func (r *NodeGroupDemandReconciler) mapAllDemands(ctx context.Context, _ client.Object) []reconcile.Request {
	requests := []reconcile.Request{}
	for _, listGVK := range []schema.GroupVersionKind{platformDemandList, demandList} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(listGVK)
		if err := r.List(ctx, list); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "list demands for event fan-out", "gvk", listGVK.String())
			continue
		}
		for i := range list.Items {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return requests
}

func (r *NodeGroupDemandReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("ngd", request.NamespacedName)
	formal := request.Namespace == ""
	demandType := demandGVK
	if formal {
		demandType = platformDemandGVK
	}
	demand := newUnstructured(demandType)
	if err := r.Get(ctx, request.NamespacedName, demand); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if formal {
		return r.reconcilePlatformDemand(ctx, demand)
	}

	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Nodes: %w", err)
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Pods: %w", err)
	}
	staticStatus, ready := r.StaticSnapshots.Current()
	if !ready {
		return r.degradeAndRetry(ctx, demand, "StaticSnapshotNotReady", "waiting for independent Node static snapshot synchronization")
	}
	staticID := staticStatus.SnapshotID
	stateID, stateCapturedAt, schedulerState, err := buildSchedulerState(nodes.Items, pods.Items)
	if err != nil {
		return ctrl.Result{}, err
	}

	spec, _, _ := unstructured.NestedMap(demand.Object, "spec")
	taskRef := mapValue(spec, "taskRef")
	task, err := r.getTask(ctx, request.Namespace, taskRef)
	if apierrors.IsNotFound(err) {
		if statusErr := r.setDemandStatus(ctx, demand, "Pending", "TaskNotFound", "waiting for referenced workload", nil, time.Now().Add(defaultTaskPoll)); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: defaultTaskPoll}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	requestedUID := stringValue(taskRef, "uid")
	if requestedUID != "" && requestedUID != string(task.GetUID()) {
		return r.degradeAndRetry(ctx, demand, "TaskUIDMismatch", "taskRef.uid does not match the live workload UID")
	}

	algorithm := AlgorithmClient{BaseURL: strings.TrimRight(r.AlgorithmURL, "/"), Client: r.httpClient(), Recorder: r.AlgorithmRecorder}
	grant := newUnstructured(grantGVK)
	// 联通 NGG 是 Cluster-scoped；名称带上来源 Namespace，避免不同租户同名 NGD 冲突。
	grantKey := types.NamespacedName{Name: grantName(request.Namespace + "-" + request.Name)}
	grantErr := r.Get(ctx, grantKey, grant)
	if grantErr != nil && !apierrors.IsNotFound(grantErr) {
		return ctrl.Result{}, grantErr
	}
	var existing *unstructured.Unstructured
	if grantErr == nil {
		existing = grant
	}

	policy := grantPolicy(spec)

	requestID := fmt.Sprintf("%s-generation-%d-state-%s", demand.GetUID(), demand.GetGeneration(), shortHash(stateID))
	algorithmRequest := map[string]any{
		"requestId": requestID, "taskUID": string(task.GetUID()), "ngdUID": string(demand.GetUID()),
		"ngdGeneration": demand.GetGeneration(), "nodeStaticSnapshotId": staticID,
		"schedulerStateSnapshotId": stateID, "schedulerStateCapturedAt": stateCapturedAt,
		"podSets": spec["podSets"], "nodeRequirements": mapOrEmpty(spec, "nodeRequirements"),
		"topologyRequirement": mapOrDefault(spec, "topologyRequirement", map[string]any{"level": "leafGroup", "mode": "same"}),
		"maxCandidateGroups":  policy.MaxCandidateGroups,
		"schedulerState":      schedulerState,
	}
	response, err := algorithm.calculate(ctx, algorithmRequest, time.Duration(policy.AlgorithmTimeoutSeconds)*time.Second)
	if err != nil {
		return r.degradeAndRetry(ctx, demand, "AlgorithmRequestFailed", err.Error())
	}
	if err := validateResponse(response, requestID, demand, task.GetUID(), staticID, stateID, staticStatus.AlgorithmBootID, schedulerState); err != nil {
		return r.degradeAndRetry(ctx, demand, "AlgorithmResponseInvalid", err.Error())
	}

	if len(response.CandidateNodeGroups) == 0 {
		if existing != nil {
			if err := r.setPlatformGrantReturned(ctx, existing); err != nil {
				return ctrl.Result{}, err
			}
		}
		if err := r.setDemandStatus(ctx, demand, "Unsatisfied", "NoFeasibleGroup", "candidateGroups=0", nil, time.Time{}); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Algorithm returned no feasible group")
		return ctrl.Result{}, nil
	}

	now := time.Now().UTC()
	desiredSpec := buildPlatformGrantSpec(demand, spec, response, nodes.Items, now)
	applied, err := r.upsertGrant(ctx, demand, existing, desiredSpec)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.setPlatformGrantStatus(ctx, applied, response.CandidateNodeGroups[0], nodes.Items); err != nil {
		return ctrl.Result{}, err
	}
	message := fmt.Sprintf("selectedGroup=%s candidates=%d nodes=%d", response.CandidateNodeGroups[0].GroupID, len(response.CandidateNodeGroups), len(response.CandidateNodeGroups[0].Nodes))
	if err := r.setDemandStatus(ctx, demand, "Fulfilled", "GrantPublished", message, applied, time.Time{}); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("published platform NGG", "candidateCount", len(response.CandidateNodeGroups), "selectedGroup", response.CandidateNodeGroups[0].GroupID, "nodeCount", len(response.CandidateNodeGroups[0].Nodes))
	// 联通协议要求持续刷新 timestamp/nodes；Node、Pod、NNT Watch 仍会提前触发。
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

// reconcilePlatformDemand handles the formal, cluster-scoped China Unicom
// resource-pool contract. Unlike the legacy task mode it does not resolve a
// VolcanoJob/Kubernetes Job: the NGD UID itself is the request identity.
func (r *NodeGroupDemandReconciler) reconcilePlatformDemand(ctx context.Context, demand *unstructured.Unstructured) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("formalNGD", demand.GetName())
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
	staticStatus, ready := r.StaticSnapshots.Current()
	if !ready {
		return r.failPlatformDemand(ctx, demand, "StaticSnapshotNotReady", "waiting for independent Node static snapshot synchronization", true)
	}
	staticID := staticStatus.SnapshotID
	stateID, stateCapturedAt, schedulerState, err := buildSchedulerState(nodes.Items, pods.Items)
	if err != nil {
		return ctrl.Result{}, err
	}

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
	// 联通 NGD spec 作为一个完整对象原样传给 Algorithm。Algorithm 当前只读取
	// nodeSelector、topologyRequirement、maxCandidateGroups、maxNodes、quota 和
	// minResources；其余字段仍保留在协议中，便于后续兼容而不需要再次改 PRC。
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
	policy := grantPolicy(spec)
	response, err := algorithm.calculate(ctx, algorithmRequest, time.Duration(policy.AlgorithmTimeoutSeconds)*time.Second)
	if err != nil {
		return r.failPlatformDemand(ctx, demand, "AlgorithmRequestFailed", err.Error(), true)
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
	now := time.Now().UTC()
	desiredSpec := buildPlatformGrantSpec(demand, spec, response, nodes.Items, now)
	applied, err := r.upsertGrant(ctx, demand, existing, desiredSpec)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.setPlatformGrantStatus(ctx, applied, selected, nodes.Items); err != nil {
		return ctrl.Result{}, err
	}
	message := fmt.Sprintf("selectedGroup=%s nodes=%d", selected.GroupID, len(selected.Nodes))
	if err := r.setPlatformDemandStatus(ctx, demand, "Fulfilled", applied.GetName(), int64(len(selected.Nodes)), message); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("published formal platform NGG", "selectedGroup", selected.GroupID, "nodeCount", len(selected.Nodes))
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

func validatePlatformDemandSpec(spec map[string]any) error {
	if stringValue(spec, "schedulerName") == "" {
		return fmt.Errorf("spec.schedulerName is required")
	}
	// minThroughput、IP亲和、网络可达性、preferredSubnet和algorithms均保留
	// 在原始NGD中并透传给Algorithm，但当前阶段不参与计算。
	topology := mapValue(spec, "topologyRequirement")
	if len(topology) > 0 {
		if profile := stringValue(topology, "profile"); profile != "leaf-border-core-v1" {
			return fmt.Errorf("spec.topologyRequirement.profile must be leaf-border-core-v1")
		}
		if strategy := stringValue(topology, "strategy"); strategy != "NarrowestFit" {
			return fmt.Errorf("spec.topologyRequirement.strategy must be NarrowestFit")
		}
		switch stringValue(topology, "widestAllowedLevel") {
		case "leafSwitch", "borderSwitch", "coreSwitch":
		default:
			return fmt.Errorf("spec.topologyRequirement.widestAllowedLevel must be leafSwitch, borderSwitch or coreSwitch")
		}
	}
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
	return nil
}

func (r *NodeGroupDemandReconciler) setPlatformDemandStatus(ctx context.Context, demand *unstructured.Unstructured, phase, grantRef string, count int64, message string) error {
	status := map[string]any{
		"phase": phase, "grantRef": grantRef, "resolvedNodeCount": count,
		"lastUpdated": time.Now().UTC().Format(time.RFC3339Nano), "message": message,
	}
	return patchStatus(ctx, r.Client, demand, status)
}

func (r *NodeGroupDemandReconciler) failPlatformDemand(ctx context.Context, demand *unstructured.Unstructured, reason, message string, retry bool) (ctrl.Result, error) {
	if err := r.setPlatformDemandStatus(ctx, demand, "Failed", "", 0, reason+": "+message); err != nil {
		return ctrl.Result{}, err
	}
	if retry {
		return ctrl.Result{RequeueAfter: defaultRetry}, nil
	}
	return ctrl.Result{}, nil
}

type policyValues struct {
	TTLSeconds                 int64
	RefreshBeforeSeconds       int64
	AlgorithmTimeoutSeconds    int64
	MaxCandidateGroups         int64
	GroupAttemptTimeoutSeconds int64
}

func grantPolicy(spec map[string]any) policyValues {
	policy := mapValue(spec, "grantPolicy")
	legacy := mapValue(spec, "candidatePolicy")
	return policyValues{
		TTLSeconds:                 intOr(policy, "ttlSeconds", intOr(legacy, "grantTTLSeconds", 600)),
		RefreshBeforeSeconds:       intOr(policy, "refreshBeforeSeconds", 120),
		AlgorithmTimeoutSeconds:    intOr(policy, "algorithmTimeoutSeconds", 5),
		MaxCandidateGroups:         clamp(intOr(policy, "maxCandidateGroups", 3), 1, 3),
		GroupAttemptTimeoutSeconds: intOr(policy, "groupAttemptTimeoutSeconds", 30),
	}
}

func (r *NodeGroupDemandReconciler) handleExisting(
	ctx context.Context,
	demand, grant *unstructured.Unstructured,
	taskPods []corev1.Pod,
	nodes []corev1.Node,
	staticID, stateID string,
	policy policyValues,
) (bool, ctrl.Result, error) {
	phase := nestedString(grant.Object, "status", "phase")
	activeState := nestedString(grant.Object, "status", "activeGroupState")
	bound := boundPodCount(taskPods)
	now := time.Now().UTC()

	if phase == "Inactive" && activeState == "Exhausted" {
		dataStatic := nestedString(grant.Object, "spec", "dataVersions", "nodeStaticSnapshotId")
		dataState := nestedString(grant.Object, "spec", "dataVersions", "schedulerStateSnapshotId")
		if dataStatic != staticID || dataState != stateID {
			return false, ctrl.Result{}, nil
		}
		// Fail closed and remain quiescent while Kubernetes/topology content is
		// unchanged. Node, Pod and NNT watches enqueue the NGD; only a changed
		// content hash causes a new Algorithm calculation.
		return true, ctrl.Result{}, nil
	}
	if phase != "Active" {
		return false, ctrl.Result{}, nil
	}

	invalid := activeGroupInvalid(grant, nodes)
	if activeState == "Locked" || bound > 0 {
		reason := "ActiveGroupLocked"
		message := fmt.Sprintf("boundPodCount=%d", bound)
		demandPhase := "Fulfilled"
		if invalid {
			reason = "LockedGroupDegraded"
			message = fmt.Sprintf("boundPodCount=%d; active group contains unavailable Node", bound)
			demandPhase = "Degraded"
		}
		if err := r.setGrantStatus(ctx, grant, "Active", "Locked", taskPods, nestedString(grant.Object, "status", "attemptStartedAt"), reason, message); err != nil {
			return true, ctrl.Result{}, err
		}
		if err := r.setDemandStatus(ctx, demand, demandPhase, reason, message, grant, time.Time{}); err != nil {
			return true, ctrl.Result{}, err
		}
		return true, ctrl.Result{}, nil
	}

	dataStatic := nestedString(grant.Object, "spec", "dataVersions", "nodeStaticSnapshotId")
	dataState := nestedString(grant.Object, "spec", "dataVersions", "schedulerStateSnapshotId")
	validUntil := parseTime(nestedString(grant.Object, "spec", "validUntil"))
	refreshAt := validUntil.Add(-time.Duration(policy.RefreshBeforeSeconds) * time.Second)
	if invalid || dataStatic != staticID || dataState != stateID || (!validUntil.IsZero() && !now.Before(refreshAt)) {
		return false, ctrl.Result{}, nil
	}

	nonterminal := 0
	for i := range taskPods {
		if taskPods[i].Status.Phase != corev1.PodSucceeded && taskPods[i].Status.Phase != corev1.PodFailed {
			nonterminal++
		}
	}
	attemptStarted := parseTime(nestedString(grant.Object, "status", "attemptStartedAt"))
	if nonterminal == 0 {
		attemptStarted = time.Time{}
	} else if attemptStarted.IsZero() {
		attemptStarted = now
	}
	timeout := time.Duration(policy.GroupAttemptTimeoutSeconds) * time.Second
	if !attemptStarted.IsZero() && now.Sub(attemptStarted) >= timeout {
		return r.advanceGroup(ctx, demand, grant, taskPods, now)
	}
	attemptText := ""
	message := "waitingForTaskPods"
	requeue := defaultPodPoll
	if !attemptStarted.IsZero() {
		attemptText = attemptStarted.Format(time.RFC3339)
		message = fmt.Sprintf("waitingForBind timeoutSeconds=%d", policy.GroupAttemptTimeoutSeconds)
		remaining := timeout - now.Sub(attemptStarted)
		if remaining > 0 && remaining < requeue {
			requeue = remaining
		}
	}
	if err := r.setGrantStatus(ctx, grant, "Active", "Trying", taskPods, attemptText, "ActiveGroupTrying", message); err != nil {
		return true, ctrl.Result{}, err
	}
	if err := r.setDemandStatus(ctx, demand, "Fulfilled", "ActiveGroupTrying", message, grant, time.Time{}); err != nil {
		return true, ctrl.Result{}, err
	}
	return true, ctrl.Result{RequeueAfter: requeue}, nil
}

func (r *NodeGroupDemandReconciler) advanceGroup(ctx context.Context, demand, grant *unstructured.Unstructured, taskPods []corev1.Pod, now time.Time) (bool, ctrl.Result, error) {
	if boundPodCount(taskPods) > 0 {
		return false, ctrl.Result{}, nil
	}
	groups, _, _ := unstructured.NestedSlice(grant.Object, "spec", "candidateNodeGroups")
	activeRank := nestedInt64(grant.Object, "spec", "activeGroupRef", "rank")
	if activeRank < int64(len(groups)) {
		next, _ := groups[activeRank].(map[string]any)
		base := grant.DeepCopy()
		_ = unstructured.SetNestedMap(grant.Object, map[string]any{"rank": activeRank + 1, "groupId": stringValue(next, "groupId")}, "spec", "activeGroupRef")
		_ = unstructured.SetNestedField(grant.Object, nestedInt64(grant.Object, "spec", "revision")+1, "spec", "revision")
		_ = unstructured.SetNestedField(grant.Object, now.Format(time.RFC3339), "spec", "generatedAt")
		if err := r.Patch(ctx, grant, client.MergeFrom(base)); err != nil {
			return true, ctrl.Result{}, err
		}
		if err := r.Get(ctx, client.ObjectKeyFromObject(grant), grant); err != nil {
			return true, ctrl.Result{}, err
		}
		message := fmt.Sprintf("advancedToRank=%d", activeRank+1)
		if err := r.setGrantStatus(ctx, grant, "Active", "Trying", taskPods, now.Format(time.RFC3339), "ActiveGroupAdvanced", message); err != nil {
			return true, ctrl.Result{}, err
		}
		if err := r.setDemandStatus(ctx, demand, "Fulfilled", "ActiveGroupAdvanced", message, grant, time.Time{}); err != nil {
			return true, ctrl.Result{}, err
		}
		ctrl.LoggerFrom(ctx).Info("advanced active group", "rank", activeRank+1, "group", stringValue(next, "groupId"))
		return true, ctrl.Result{RequeueAfter: defaultPodPoll}, nil
	}
	message := fmt.Sprintf("attemptedGroups=%d", len(groups))
	if err := r.setGrantStatus(ctx, grant, "Inactive", "Exhausted", taskPods, nestedString(grant.Object, "status", "attemptStartedAt"), "CandidateGroupsExhausted", message); err != nil {
		return true, ctrl.Result{}, err
	}
	if err := r.setDemandStatus(ctx, demand, "Unsatisfied", "CandidateGroupsExhausted", message, grant, time.Time{}); err != nil {
		return true, ctrl.Result{}, err
	}
	return true, ctrl.Result{}, nil
}

func (r *NodeGroupDemandReconciler) getTask(ctx context.Context, namespace string, reference map[string]any) (*unstructured.Unstructured, error) {
	apiVersion := stringValue(reference, "apiVersion")
	kind := stringValue(reference, "kind")
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, fmt.Errorf("parse task apiVersion: %w", err)
	}
	task := newUnstructured(gv.WithKind(kind))
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: stringValue(reference, "name")}, task); err != nil {
		return nil, err
	}
	return task, nil
}

func (r *NodeGroupDemandReconciler) upsertGrant(ctx context.Context, demand, existing *unstructured.Unstructured, spec map[string]any) (*unstructured.Unstructured, error) {
	if existing == nil {
		grant := newUnstructured(grantGVK)
		demandRef := demand.GetName()
		grantSourceName := demand.GetName()
		if demand.GetNamespace() != "" {
			demandRef = demand.GetNamespace() + "/" + demand.GetName()
			grantSourceName = demand.GetNamespace() + "-" + demand.GetName()
		}
		grant.SetName(grantName(grantSourceName))
		// Cluster-scoped NGG 不能 ownerReference 到 namespaced 旧 NGD；用 demandRef 和标签追溯。
		grant.SetLabels(map[string]string{
			grantGroup + "/demand-uid": shortHash(string(demand.GetUID())),
			grantGroup + "/scheduler":  stringValue(spec, "schedulerName"),
		})
		grant.SetAnnotations(map[string]string{grantGroup + "/demand-ref": demandRef})
		_ = unstructured.SetNestedMap(grant.Object, spec, "spec")
		if err := r.Create(ctx, grant); err != nil {
			return nil, fmt.Errorf("create NGG: %w", err)
		}
		return grant, nil
	}
	base := existing.DeepCopy()
	_ = unstructured.SetNestedMap(existing.Object, spec, "spec")
	if err := r.Patch(ctx, existing, client.MergeFrom(base)); err != nil {
		return nil, fmt.Errorf("update NGG: %w", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(existing), existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// buildPlatformGrantSpec 将 Algorithm 排名第一的拓扑组转换为联通正式 NGG 契约。
// PRC 不重新排序、不修改正常模式分数；降级/关闭负载感知时按契约使用中性分 50。
func buildPlatformGrantSpec(demand *unstructured.Unstructured, demandSpec map[string]any, response AlgorithmResponse, liveNodes []corev1.Node, now time.Time) map[string]any {
	selected := response.CandidateNodeGroups[0]
	byName := make(map[string]*corev1.Node, len(liveNodes))
	for i := range liveNodes {
		byName[liveNodes[i].Name] = &liveNodes[i]
	}
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
		if node := byName[candidate.NodeName]; node != nil {
			if topology := platformTopology(node.Labels); len(topology) > 0 {
				item["topology"] = topology
			}
		}
		items = append(items, item)
	}
	timestamp := response.MetricSnapshotCapturedAt
	if timestamp == "" {
		timestamp = now.Format(time.RFC3339Nano)
	}
	demandRef := demand.GetName()
	if demand.GetNamespace() != "" {
		demandRef = demand.GetNamespace() + "/" + demand.GetName()
	}
	return map[string]any{
		"schedulerName": stringValue(demandSpec, "schedulerName"),
		"version":       "v1", "timestamp": timestamp, "source": source,
		"demandRef": demandRef,
		"nodes":     items,
	}
}

func platformTopology(labels map[string]string) map[string]any {
	const demoPrefix = "topology.demo.ngg.io/"
	value := map[string]any{}
	fields := map[string]string{
		"dataCenter":        labels["topology.kubernetes.io/region"],
		"convergenceSwitch": labels[demoPrefix+"border-switch"],
		"accessSwitch":      labels[demoPrefix+"leaf-switch"],
		"subnet":            labels[demoPrefix+"subnet"],
		"rack":              labels["topology.kubernetes.io/rack"],
	}
	for name, item := range fields {
		if item != "" {
			value[name] = item
		}
	}
	return value
}

func (r *NodeGroupDemandReconciler) setPlatformGrantStatus(ctx context.Context, grant *unstructured.Unstructured, selected CandidateGroup, liveNodes []corev1.Node) error {
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
	// status.consumer 由调度侧写入；PRC 更新自身字段时必须原样保留。
	if consumer, found, _ := unstructured.NestedMap(grant.Object, "status", "consumer"); found {
		status["consumer"] = consumer
	}
	return patchStatus(ctx, r.Client, grant, status)
}

func (r *NodeGroupDemandReconciler) setPlatformGrantReturned(ctx context.Context, grant *unstructured.Unstructured) error {
	status := map[string]any{
		"phase":            "Returned",
		"resolvedCapacity": map[string]any{"nodes": int64(0), "cpu": "0", "memory": "0"},
	}
	if consumer, found, _ := unstructured.NestedMap(grant.Object, "status", "consumer"); found {
		status["consumer"] = consumer
	}
	return patchStatus(ctx, r.Client, grant, status)
}

func buildGrantSpec(demand, task *unstructured.Unstructured, demandSpec map[string]any, response AlgorithmResponse, policy policyValues, revision int64, now time.Time) map[string]any {
	groups := make([]any, 0, len(response.CandidateNodeGroups))
	for _, group := range response.CandidateNodeGroups {
		nodes := make([]any, 0, len(group.Nodes))
		for _, node := range group.Nodes {
			nodes = append(nodes, map[string]any{"name": node.NodeName, "uid": node.NodeUID, "score": node.Score})
		}
		groups = append(groups, map[string]any{
			"rank": group.Rank, "groupId": group.GroupID, "topologyLevel": group.TopologyLevel,
			"groupScore": group.GroupScore, "nodes": nodes,
		})
	}
	taskRef := mapValue(demandSpec, "taskRef")
	spec := map[string]any{
		"demandRef":     map[string]any{"name": demand.GetName(), "uid": string(demand.GetUID()), "generation": demand.GetGeneration()},
		"taskRef":       map[string]any{"apiVersion": stringValue(taskRef, "apiVersion"), "kind": stringValue(taskRef, "kind"), "name": task.GetName(), "uid": string(task.GetUID())},
		"schedulerName": stringValue(demandSpec, "schedulerName"), "source": "prc-algorithm",
		"candidateNodeGroups": groups,
		"groupAttemptPolicy":  map[string]any{"timeoutSeconds": policy.GroupAttemptTimeoutSeconds, "lockAfterFirstBind": true},
		"dataVersions": map[string]any{
			"algorithmBootId": response.AlgorithmBootID, "nodeStaticSnapshotId": response.NodeStaticSnapshotID,
			"schedulerStateSnapshotId": response.SchedulerStateSnapshotID, "metricSnapshotId": response.MetricSnapshotID,
		},
		"revision": revision, "generatedAt": now.Format(time.RFC3339),
		"validUntil": now.Add(time.Duration(policy.TTLSeconds) * time.Second).Format(time.RFC3339),
	}
	if len(response.CandidateNodeGroups) > 0 {
		spec["activeGroupRef"] = map[string]any{"rank": int64(1), "groupId": response.CandidateNodeGroups[0].GroupID}
	}
	return spec
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

func (r *NodeGroupDemandReconciler) setGrantStatus(ctx context.Context, grant *unstructured.Unstructured, phase, state string, pods []corev1.Pod, attempt, reason, message string) error {
	active, _, _ := unstructured.NestedMap(grant.Object, "spec", "activeGroupRef")
	groups, _, _ := unstructured.NestedSlice(grant.Object, "spec", "candidateNodeGroups")
	activeNodes := int64(0)
	for _, raw := range groups {
		item, _ := raw.(map[string]any)
		if stringValue(item, "groupId") == stringValue(active, "groupId") && intValue(item, "rank") == intValue(active, "rank") {
			nodes, _ := item["nodes"].([]any)
			activeNodes = int64(len(nodes))
		}
	}
	status := map[string]any{
		"phase": phase, "observedGeneration": grant.GetGeneration(),
		"observedRevision": nestedInt64(grant.Object, "spec", "revision"),
		"activeGroupState": state, "activeGroupId": stringValue(active, "groupId"),
		"activeGroupRank": intValue(active, "rank"), "activeNodeCount": activeNodes,
		"boundPodCount": boundPodCount(pods), "attemptStartedAt": attempt,
		"conditions": []any{condition(reason, message)},
	}
	return patchStatus(ctx, r.Client, grant, status)
}

func (r *NodeGroupDemandReconciler) setDemandStatus(ctx context.Context, demand *unstructured.Unstructured, phase, reason, message string, grant *unstructured.Unstructured, nextRetry time.Time) error {
	status := map[string]any{
		"phase": phase, "observedGeneration": demand.GetGeneration(),
		"conditions": []any{condition(reason, message)}, "lastReconcileTime": time.Now().UTC().Format(time.RFC3339),
	}
	if grant != nil {
		status["grantRef"] = map[string]any{"name": grant.GetName(), "uid": string(grant.GetUID()), "revision": nestedInt64(grant.Object, "spec", "revision")}
		status["taskUID"] = nestedString(grant.Object, "spec", "taskRef", "uid")
	}
	if !nextRetry.IsZero() {
		status["nextRetryAt"] = nextRetry.UTC().Format(time.RFC3339)
	}
	return patchStatus(ctx, r.Client, demand, status)
}

func (r *NodeGroupDemandReconciler) degradeAndRetry(ctx context.Context, demand *unstructured.Unstructured, reason, message string) (ctrl.Result, error) {
	next := time.Now().Add(defaultRetry)
	if err := r.setDemandStatus(ctx, demand, "Degraded", reason, message, nil, next); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: defaultRetry}, nil
}

func patchStatus(ctx context.Context, c client.Client, object *unstructured.Unstructured, status map[string]any) error {
	base := object.DeepCopy()
	if err := unstructured.SetNestedMap(object.Object, status, "status"); err != nil {
		return err
	}
	if err := c.Status().Patch(ctx, object, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch %s/%s status: %w", object.GetKind(), object.GetName(), err)
	}
	return nil
}

func condition(reason, message string) map[string]any {
	return map[string]any{"type": "Ready", "status": "True", "reason": reason, "message": message, "lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}
}

func grantMatches(grant, demand *unstructured.Unstructured, taskUID types.UID) bool {
	return nestedString(grant.Object, "spec", "demandRef", "uid") == string(demand.GetUID()) &&
		nestedInt64(grant.Object, "spec", "demandRef", "generation") == demand.GetGeneration() &&
		nestedString(grant.Object, "spec", "taskRef", "uid") == string(taskUID)
}

func activeGroupInvalid(grant *unstructured.Unstructured, nodes []corev1.Node) bool {
	allowed := activeGroupNodes(grant)
	if len(allowed) == 0 {
		return true
	}
	live := map[string]*corev1.Node{}
	for i := range nodes {
		live[nodes[i].Name] = &nodes[i]
	}
	for name, uid := range allowed {
		node := live[name]
		if node == nil || node.UID != uid || !nodeReady(node) || node.Spec.Unschedulable {
			return true
		}
	}
	return false
}

func activeGroupNodes(grant *unstructured.Unstructured) map[string]types.UID {
	activeID := nestedString(grant.Object, "spec", "activeGroupRef", "groupId")
	activeRank := nestedInt64(grant.Object, "spec", "activeGroupRef", "rank")
	groups, _, _ := unstructured.NestedSlice(grant.Object, "spec", "candidateNodeGroups")
	result := map[string]types.UID{}
	for _, raw := range groups {
		item, _ := raw.(map[string]any)
		if stringValue(item, "groupId") != activeID || intValue(item, "rank") != activeRank {
			continue
		}
		nodes, _ := item["nodes"].([]any)
		for _, rawNode := range nodes {
			node, _ := rawNode.(map[string]any)
			result[stringValue(node, "name")] = types.UID(stringValue(node, "uid"))
		}
	}
	return result
}

func boundPodCount(pods []corev1.Pod) int64 {
	var count int64
	for i := range pods {
		if pods[i].Spec.NodeName != "" {
			count++
		}
	}
	return count
}

func (r *NodeGroupDemandReconciler) httpClient() *http.Client {
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

func nestedString(object map[string]any, fields ...string) string {
	value, _, _ := unstructured.NestedString(object, fields...)
	return value
}

func nestedInt64(object map[string]any, fields ...string) int64 {
	value, _, _ := unstructured.NestedInt64(object, fields...)
	return value
}

func mapValue(object map[string]any, key string) map[string]any {
	value, _ := object[key].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func mapOrEmpty(object map[string]any, key string) map[string]any { return mapValue(object, key) }

func mapOrDefault(object map[string]any, key string, fallback map[string]any) map[string]any {
	value := mapValue(object, key)
	if len(value) == 0 {
		return fallback
	}
	return value
}

func sliceOrEmpty(object map[string]any, key string) []any {
	value, _ := object[key].([]any)
	if value == nil {
		return []any{}
	}
	return value
}

func stringValue(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func intValue(object map[string]any, key string) int64 {
	switch value := object[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		result, _ := value.Int64()
		return result
	default:
		return 0
	}
}

func intOr(object map[string]any, key string, fallback int64) int64 {
	if value := intValue(object, key); value != 0 {
		return value
	}
	return fallback
}

func clamp(value, minimum, maximum int64) int64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func parseTime(value string) time.Time {
	result, _ := time.Parse(time.RFC3339, value)
	return result
}

// Stable key helper used by tests and logging.
func sortedNodeNames(nodes map[string]types.UID) []string {
	result := make([]string, 0, len(nodes))
	for name := range nodes {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

var _ = demandAnnotation
var _ = maxAlgorithmBodyBytes
