// static_snapshot_controller.go 将Node静态数据同步从任务级NGD Reconcile中拆出。
package controller

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	staticSnapshotControllerName = "node-static-snapshot"
	staticSnapshotSingletonName  = "cluster-static-snapshot"
	defaultStaticResync          = 15 * time.Second
)

// StaticSnapshotStatus是PRC最近一次获得Algorithm确认的静态快照状态。
type StaticSnapshotStatus struct {
	SnapshotID      string    `json:"snapshotId"`
	AlgorithmBootID string    `json:"algorithmBootId"`
	NodeCount       int       `json:"nodeCount"`
	SyncedAt        time.Time `json:"syncedAt"`
	LastError       string    `json:"lastError,omitempty"`
	LastAttemptedAt time.Time `json:"lastAttemptedAt"`
}

// StaticSnapshotState在独立静态控制器和任务Reconciler之间共享已确认的快照身份。
// 它不缓存Node业务对象，完整静态数据由Algorithm进程内缓存持有。
type StaticSnapshotState struct {
	mu     sync.RWMutex
	status StaticSnapshotStatus
	ready  bool
}

func NewStaticSnapshotState() *StaticSnapshotState { return &StaticSnapshotState{} }

// Current返回最近一次被Algorithm确认的快照；未Ready时第二个返回值为false。
func (s *StaticSnapshotState) Current() (StaticSnapshotStatus, bool) {
	if s == nil {
		return StaticSnapshotStatus{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status, s.ready
}

func (s *StaticSnapshotState) confirmed(snapshotID, bootID string, nodeCount int, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = StaticSnapshotStatus{
		SnapshotID: snapshotID, AlgorithmBootID: bootID, NodeCount: nodeCount,
		SyncedAt: now, LastAttemptedAt: now,
	}
	s.ready = true
}

// syncing在新Hash尚未被Algorithm确认时立即关闭任务读取，避免任务与静态更新并发时使用旧快照。
func (s *StaticSnapshotState) syncing(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.LastAttemptedAt = now
	s.status.LastError = ""
	s.ready = false
}

func (s *StaticSnapshotState) failed(err error, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.LastAttemptedAt = now
	if err != nil {
		s.status.LastError = err.Error()
	}
}

// WaitForReady供启动检查和测试使用；等待过程不触发任何同步动作。
func (s *StaticSnapshotState) WaitForReady(ctx context.Context) (StaticSnapshotStatus, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if status, ready := s.Current(); ready {
			return status, nil
		}
		select {
		case <-ctx.Done():
			status, _ := s.Current()
			return status, fmt.Errorf("wait for static snapshot: %w; lastError=%s", ctx.Err(), status.LastError)
		case <-ticker.C:
		}
	}
}

// NodeStaticSnapshotReconciler只监听Node静态信息变化，并独立维护Algorithm静态缓存。
type NodeStaticSnapshotReconciler struct {
	client.Client
	AlgorithmURL      string
	ClusterID         string
	HTTPClient        *http.Client
	State             *StaticSnapshotState
	AlgorithmRecorder AlgorithmExchangeRecorder
	ResyncInterval    time.Duration
}

// SetupWithManager把所有Node事件折叠成同一个集群级Reconcile Key。
func (r *NodeStaticSnapshotReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.State == nil {
		return fmt.Errorf("StaticSnapshotState is required")
	}
	if r.ResyncInterval <= 0 {
		r.ResyncInterval = defaultStaticResync
	}
	singleton := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: staticSnapshotSingletonName}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named(staticSnapshotControllerName).
		Watches(&corev1.Node{}, singleton, builder.WithPredicates(staticNodeChangePredicate())).
		Complete(r)
}

// staticNodeChangePredicate忽略仅ResourceVersion/心跳变化的Node事件，避免大集群反复全量构造静态快照。
func staticNodeChangePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(update event.UpdateEvent) bool {
			oldNode, oldOK := update.ObjectOld.(*corev1.Node)
			newNode, newOK := update.ObjectNew.(*corev1.Node)
			if !oldOK || !newOK {
				return true
			}
			return oldNode.UID != newNode.UID ||
				!reflect.DeepEqual(oldNode.Labels, newNode.Labels) ||
				staticTopologyAnnotationsChanged(oldNode.Annotations, newNode.Annotations) ||
				!reflect.DeepEqual(oldNode.Status.Allocatable, newNode.Status.Allocatable)
		},
	}
}

func (r *NodeStaticSnapshotReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	started := time.Now()
	now := time.Now().UTC()
	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		r.State.failed(err, now)
		log.Error(err, "failed to list Nodes for static snapshot")
		return ctrl.Result{}, fmt.Errorf("list Nodes for static snapshot: %w", err)
	}
	if len(nodes.Items) == 0 {
		err := fmt.Errorf("static snapshot requires at least one Node")
		r.State.failed(err, now)
		log.Info("static snapshot is not ready", "reason", err.Error(), "retryAfter", r.ResyncInterval)
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	staticID, staticBody, err := buildStaticSnapshot(r.ClusterID, nodes.Items)
	if err != nil {
		r.State.failed(err, now)
		log.Error(err, "failed to build Node static snapshot", "listedNodeCount", len(nodes.Items), "retryAfter", r.ResyncInterval)
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	algorithm := AlgorithmClient{
		BaseURL: strings.TrimRight(r.AlgorithmURL, "/"), Client: r.httpClient(), Recorder: r.AlgorithmRecorder,
	}
	current, ready := r.State.Current()
	if ready && current.SnapshotID == staticID {
		remote, statusErr := algorithm.staticStatus(ctx)
		if statusErr == nil && remote.Ready && remote.AcceptedSnapshot == staticID && remote.AlgorithmBootID == current.AlgorithmBootID {
			r.State.confirmed(staticID, remote.AlgorithmBootID, len(nodes.Items), now)
			log.V(1).Info("static snapshot remains synchronized",
				"snapshotId", shortHash(staticID), "nodeCount", len(staticBody.Nodes),
				"algorithmBootId", remote.AlgorithmBootID, "elapsedMs", elapsedMilliseconds(started),
			)
			return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
		}
		if statusErr != nil {
			log.Info("Algorithm static cache status unavailable; republishing snapshot", "error", statusErr.Error(), "snapshotId", shortHash(staticID))
		} else {
			log.Info("Algorithm static cache identity changed; republishing snapshot",
				"snapshotId", shortHash(staticID), "remoteSnapshotId", shortHash(remote.AcceptedSnapshot),
				"remoteReady", remote.Ready, "algorithmBootId", remote.AlgorithmBootID,
			)
		}
		// Algorithm重启、缓存丢失或状态不可达时重新PUT。
	}

	// 新静态Hash或远端缓存身份变化期间故障关闭，直到新的PUT被明确确认。
	r.State.syncing(now)
	log.Info("publishing Node static snapshot to Algorithm Server",
		"snapshotId", shortHash(staticID), "nodeCount", len(staticBody.Nodes), "listedNodeCount", len(nodes.Items),
	)
	ack, err := algorithm.putStatic(ctx, staticID, staticBody)
	if err != nil {
		r.State.failed(err, now)
		log.Error(err, "failed to publish Node static snapshot", "snapshotId", shortHash(staticID), "elapsedMs", elapsedMilliseconds(started))
		return ctrl.Result{}, fmt.Errorf("sync static snapshot to Algorithm: %w", err)
	}
	if ack.AcceptedSnapshot != staticID || ack.AlgorithmBootID == "" {
		err := fmt.Errorf("Algorithm did not acknowledge static snapshot identity")
		r.State.failed(err, now)
		return ctrl.Result{}, err
	}
	r.State.confirmed(staticID, ack.AlgorithmBootID, len(nodes.Items), now)
	log.Info("Algorithm Server acknowledged Node static snapshot",
		"snapshotId", shortHash(staticID), "nodeCount", ack.NodeCount,
		"algorithmBootId", ack.AlgorithmBootID, "elapsedMs", elapsedMilliseconds(started),
	)
	return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
}

func staticTopologyAnnotationsChanged(oldAnnotations, newAnnotations map[string]string) bool {
	for _, key := range []string{
		"topology.demo.ngg.io/leaf-switch-ids",
		"topology.demo.ngg.io/leaf-links",
	} {
		if oldAnnotations[key] != newAnnotations[key] {
			return true
		}
	}
	return false
}

func (r *NodeStaticSnapshotReconciler) httpClient() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}
