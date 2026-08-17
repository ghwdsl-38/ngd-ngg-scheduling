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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	group                 = "scheduling.demo.ngg.io"
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
	demandGVK    = schema.GroupVersionKind{Group: group, Version: version, Kind: demandKind}
	demandList   = schema.GroupVersionKind{Group: group, Version: version, Kind: demandKind + "List"}
	grantGVK     = schema.GroupVersionKind{Group: group, Version: version, Kind: grantKind}
	topologyGVK  = schema.GroupVersionKind{Group: group, Version: version, Kind: topologyKind}
	topologyList = schema.GroupVersionKind{
		Group: group, Version: version, Kind: topologyKind + "List",
	}
)

type NodeGroupDemandReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	AlgorithmURL string
	ClusterID    string
	HTTPClient   *http.Client
}

// SetupWithManager uses the Kubebuilder/controller-runtime builder pattern.
// NGD generation changes are primary events; Node, Pod and NNT changes enqueue
// all demands so Pending/Unsatisfied/Degraded workloads are actively retried.
func (r *NodeGroupDemandReconciler) SetupWithManager(mgr ctrl.Manager) error {
	demand := newUnstructured(demandGVK)
	topology := newUnstructured(topologyGVK)
	return ctrl.NewControllerManagedBy(mgr).
		For(demand, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.mapAllDemands)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.mapAllDemands)).
		Watches(topology, handler.EnqueueRequestsFromMapFunc(r.mapAllDemands)).
		Complete(r)
}

func (r *NodeGroupDemandReconciler) mapAllDemands(ctx context.Context, _ client.Object) []reconcile.Request {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(demandList)
	if err := r.List(ctx, list); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "list demands for event fan-out")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return requests
}

func (r *NodeGroupDemandReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("ngd", request.NamespacedName)
	demand := newUnstructured(demandGVK)
	if err := r.Get(ctx, request.NamespacedName, demand); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Nodes: %w", err)
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Pods: %w", err)
	}
	topologies := &unstructured.UnstructuredList{}
	topologies.SetGroupVersionKind(topologyList)
	if err := r.List(ctx, topologies); err != nil {
		return ctrl.Result{}, fmt.Errorf("list NNT: %w", err)
	}
	staticID, staticBody, err := buildStaticSnapshot(r.ClusterID, nodes.Items, topologies.Items)
	if err != nil {
		return r.degradeAndRetry(ctx, demand, "StaticSnapshotNotReady", err.Error())
	}
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

	algorithm := AlgorithmClient{BaseURL: strings.TrimRight(r.AlgorithmURL, "/"), Client: r.httpClient()}
	ack, err := algorithm.putStatic(ctx, staticID, staticBody)
	if err != nil {
		return r.degradeAndRetry(ctx, demand, "AlgorithmUnavailable", err.Error())
	}
	if ack.AcceptedSnapshot != staticID {
		return r.degradeAndRetry(ctx, demand, "SnapshotNotAcknowledged", "Algorithm did not acknowledge static snapshot")
	}

	grant := newUnstructured(grantGVK)
	grantKey := types.NamespacedName{Namespace: request.Namespace, Name: grantName(request.Name)}
	grantErr := r.Get(ctx, grantKey, grant)
	if grantErr != nil && !apierrors.IsNotFound(grantErr) {
		return ctrl.Result{}, grantErr
	}
	var existing *unstructured.Unstructured
	if grantErr == nil {
		existing = grant
	}

	policy := grantPolicy(spec)
	taskPods := directTaskPods(pods.Items, request.Namespace, task.GetUID())
	if existing != nil && grantMatches(existing, demand, task.GetUID()) {
		handled, result, handleErr := r.handleExisting(
			ctx, demand, existing, taskPods, nodes.Items, staticID, stateID, policy,
		)
		if handleErr != nil {
			return ctrl.Result{}, handleErr
		}
		if handled {
			return result, nil
		}
	}

	requestID := fmt.Sprintf("%s-generation-%d-state-%s", demand.GetUID(), demand.GetGeneration(), shortHash(stateID))
	algorithmRequest := map[string]any{
		"requestId": requestID, "taskUID": string(task.GetUID()), "ngdUID": string(demand.GetUID()),
		"ngdGeneration": demand.GetGeneration(), "nodeStaticSnapshotId": staticID,
		"schedulerStateSnapshotId": stateID, "schedulerStateCapturedAt": stateCapturedAt,
		"podSets": spec["podSets"], "nodeRequirements": mapOrEmpty(spec, "nodeRequirements"),
		"topologyRequirement": mapOrDefault(spec, "topologyRequirement", map[string]any{"level": "leafGroup", "mode": "same"}),
		"algorithms":          sliceOrEmpty(spec, "algorithms"), "maxCandidateGroups": policy.MaxCandidateGroups,
		"schedulerState": schedulerState,
	}
	response, err := algorithm.calculate(ctx, algorithmRequest, time.Duration(policy.AlgorithmTimeoutSeconds)*time.Second)
	if err != nil {
		return r.degradeAndRetry(ctx, demand, "AlgorithmRequestFailed", err.Error())
	}
	if err := validateResponse(response, requestID, demand, task.GetUID(), staticID, stateID, ack.AlgorithmBootID, schedulerState); err != nil {
		return r.degradeAndRetry(ctx, demand, "AlgorithmResponseInvalid", err.Error())
	}

	now := time.Now().UTC()
	revision := int64(1)
	if existing != nil {
		revision = nestedInt64(existing.Object, "spec", "revision") + 1
	}
	desiredSpec := buildGrantSpec(demand, task, spec, response, policy, revision, now)
	applied, err := r.upsertGrant(ctx, demand, existing, desiredSpec)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(response.CandidateNodeGroups) == 0 {
		if err := r.setGrantStatus(ctx, applied, "Inactive", "Exhausted", taskPods, "", "NoFeasibleGroup", "candidateGroups=0"); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.setDemandStatus(ctx, demand, "Unsatisfied", "NoFeasibleGroup", "candidateGroups=0", applied, time.Time{}); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Algorithm returned no feasible group")
		return ctrl.Result{}, nil
	}
	if err := r.setGrantStatus(ctx, applied, "Active", "Trying", taskPods, "", "ActiveGroupReady", fmt.Sprintf("candidateGroups=%d", len(response.CandidateNodeGroups))); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.setDemandStatus(ctx, demand, "Fulfilled", "ActiveGroupReady", fmt.Sprintf("candidateGroups=%d", len(response.CandidateNodeGroups)), applied, time.Time{}); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("created candidate groups", "count", len(response.CandidateNodeGroups), "active", response.CandidateNodeGroups[0].GroupID)
	return ctrl.Result{RequeueAfter: defaultPodPoll}, nil
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
		grant.SetName(grantName(demand.GetName()))
		grant.SetNamespace(demand.GetNamespace())
		controller := true
		block := true
		grant.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion: demand.GetAPIVersion(), Kind: demand.GetKind(), Name: demand.GetName(), UID: demand.GetUID(),
			Controller: &controller, BlockOwnerDeletion: &block,
		}})
		grant.SetLabels(map[string]string{
			group + "/demand": demand.GetName(), group + "/scheduler": stringValue(spec, "schedulerName"),
		})
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
	known := map[string]struct{}{}
	for _, item := range state {
		known[item.NodeUID] = struct{}{}
	}
	seen := map[string]struct{}{}
	for index, group := range response.CandidateNodeGroups {
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
			if _, ok := known[node.NodeUID]; !ok {
				return fmt.Errorf("Algorithm returned unknown Node UID %s", node.NodeUID)
			}
			if _, duplicate := seen[node.NodeUID]; duplicate {
				return fmt.Errorf("candidate groups overlap on Node UID %s", node.NodeUID)
			}
			seen[node.NodeUID] = struct{}{}
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
