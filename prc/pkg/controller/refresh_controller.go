package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controlleroptions "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	defaultDemandRefreshInterval = 15 * time.Second
	defaultRefreshConcurrency    = 5
)

// NodeGroupDemandReconciler only watches NGD lifecycle/generation events. It
// never returns RequeueAfter and never calls Algorithm Server directly.
type NodeGroupDemandReconciler struct {
	client.Client
	Scheduler *RefreshScheduler
	Processor *DemandProcessor
}

func (r *NodeGroupDemandReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Scheduler == nil || r.Processor == nil {
		return fmt.Errorf("RefreshScheduler and DemandProcessor are required")
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("node-group-demand-events").
		For(newUnstructured(platformDemandGVK), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

func (r *NodeGroupDemandReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	demand := newUnstructured(platformDemandGVK)
	if err := r.Get(ctx, request.NamespacedName, demand); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		r.Scheduler.Remove(request.NamespacedName)
		if err := r.deleteGrant(ctx, request.NamespacedName); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	created, changed := r.Scheduler.Upsert(request.NamespacedName, demand.GetUID(), demand.GetGeneration())
	status, _, _ := unstructured.NestedMap(demand.Object, "status")
	phase := stringValue(status, "phase")
	switch {
	case created && phase == "":
		if err := r.Processor.setPlatformDemandStatus(ctx, demand, "Pending", "", 0, "WaitingForInitialCalculation"); err != nil {
			return ctrl.Result{}, err
		}
	case changed:
		count, _, _ := unstructured.NestedInt64(status, "resolvedNodeCount")
		if err := r.Processor.setPlatformDemandStatus(
			ctx, demand, "Updating", stringValue(status, "grantRef"), count, "DemandSpecChanged",
		); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.Scheduler.Trigger(ctx, request.NamespacedName); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *NodeGroupDemandReconciler) deleteGrant(ctx context.Context, key types.NamespacedName) error {
	name := grantName(key.Name)
	grant := newUnstructured(grantGVK)
	grant.SetName(name)
	if err := r.Delete(ctx, grant); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete NGG %s for removed NGD: %w", name, err)
	}
	return nil
}

// RefreshReconciler is backed by controller-runtime's native WorkQueue,
// de-duplication, worker concurrency and rate-limited error retry.
type RefreshReconciler struct {
	Processor       *DemandProcessor
	Scheduler       *RefreshScheduler
	RefreshInterval time.Duration
	MaxConcurrent   int
}

func (r *RefreshReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Processor == nil || r.Scheduler == nil {
		return fmt.Errorf("DemandProcessor and RefreshScheduler are required")
	}
	if r.RefreshInterval <= 0 {
		r.RefreshInterval = defaultDemandRefreshInterval
	}
	if r.MaxConcurrent <= 0 {
		r.MaxConcurrent = defaultRefreshConcurrency
	}
	refreshSource := source.Channel(r.Scheduler.Events(), &handler.EnqueueRequestForObject{})
	return ctrl.NewControllerManagedBy(mgr).
		Named("node-group-demand-refresh").
		WatchesRawSource(refreshSource).
		WithOptions(controlleroptions.Options{MaxConcurrentReconciles: r.MaxConcurrent}).
		Complete(r)
}

func (r *RefreshReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	executionContext, uid, generation, found := r.Scheduler.Begin(ctx, request.NamespacedName)
	if !found {
		return ctrl.Result{}, nil
	}
	result, err := r.Processor.Process(executionContext, request)
	r.Scheduler.Finish(request.NamespacedName, uid, generation)
	if !r.Scheduler.IsCurrent(request.NamespacedName, uid, generation) {
		// Update/delete arrived during execution. The native queue either already
		// contains the new key or the delete path removed it.
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	delay := result.RequeueAfter
	if delay <= 0 {
		delay = r.RefreshInterval
	}
	r.Scheduler.ScheduleAfter(request.NamespacedName, uid, generation, delay)
	return ctrl.Result{}, nil
}
