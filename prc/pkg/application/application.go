// Package application assembles and owns the complete PRC controller process.
// Production main and integration tests use the same constructor so controller
// registration, shared static state and readiness semantics cannot drift.
package application

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"scheduling.demo.ngg.io/prc/pkg/controller"
	ctrl "sigs.k8s.io/controller-runtime"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// Config contains process-level dependencies and optional test observation
// hooks. Controller implementation details stay private to Application.
type Config struct {
	KubernetesConfig *rest.Config
	Scheme           *runtime.Scheme

	AlgorithmURL string
	ClusterID    string
	HTTPClient   *http.Client

	MetricsBindAddress     string
	HealthProbeBindAddress string
	LeaderElection         bool
	LeaderElectionID       string
	StaticResyncInterval   time.Duration
	DemandRefreshInterval  time.Duration
	MaxConcurrentRefreshes int
	// SkipControllerNameValidation is intended for tests that create more than
	// one complete Application sequentially in the same Go process.
	SkipControllerNameValidation bool

	DebugAlgorithmTrace bool
	AlgorithmRecorder   controller.AlgorithmExchangeRecorder
	ReconcileObserver   func(uid string, generation int64, observedAt time.Time)
}

// Application is the complete PRC runtime: one controller-runtime Manager,
// the independent static snapshot controller, the NGD controller and their
// private shared snapshot identity state.
type Application struct {
	manager         ctrl.Manager
	staticSnapshots *controller.StaticSnapshotState
}

// New creates and registers the complete PRC application without starting it.
func New(config Config) (*Application, error) {
	if config.KubernetesConfig == nil {
		return nil, fmt.Errorf("KubernetesConfig is required")
	}
	if config.AlgorithmURL == "" {
		return nil, fmt.Errorf("AlgorithmURL is required")
	}
	if config.ClusterID == "" {
		return nil, fmt.Errorf("ClusterID is required")
	}
	if config.MetricsBindAddress == "" {
		config.MetricsBindAddress = "0"
	}
	if config.HealthProbeBindAddress == "" {
		config.HealthProbeBindAddress = "0"
	}
	if config.LeaderElectionID == "" {
		config.LeaderElectionID = "ngd-ngg-prc.scheduling.platform.example.io"
	}

	scheme := config.Scheme
	if scheme == nil {
		scheme = runtime.NewScheme()
		if err := clientgoscheme.AddToScheme(scheme); err != nil {
			return nil, fmt.Errorf("add Kubernetes scheme: %w", err)
		}
	}

	skipNameValidation := config.SkipControllerNameValidation
	manager, err := ctrl.NewManager(config.KubernetesConfig, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: config.MetricsBindAddress},
		HealthProbeBindAddress: config.HealthProbeBindAddress,
		LeaderElection:         config.LeaderElection,
		LeaderElectionID:       config.LeaderElectionID,
		Controller:             controllerconfig.Controller{SkipNameValidation: &skipNameValidation},
	})
	if err != nil {
		return nil, fmt.Errorf("create PRC manager: %w", err)
	}

	staticSnapshots := controller.NewStaticSnapshotState()
	staticReconciler := &controller.NodeStaticSnapshotReconciler{
		Client: manager.GetClient(), AlgorithmURL: config.AlgorithmURL, ClusterID: config.ClusterID,
		HTTPClient: config.HTTPClient, State: staticSnapshots, AlgorithmRecorder: config.AlgorithmRecorder,
		ResyncInterval: config.StaticResyncInterval,
	}
	if err := staticReconciler.SetupWithManager(manager); err != nil {
		return nil, fmt.Errorf("setup Node static snapshot controller: %w", err)
	}

	processor := &controller.DemandProcessor{
		Client: manager.GetClient(), AlgorithmURL: config.AlgorithmURL,
		HTTPClient: config.HTTPClient, StaticSnapshots: staticSnapshots,
		DebugAlgorithmTrace: config.DebugAlgorithmTrace, AlgorithmRecorder: config.AlgorithmRecorder,
		ReconcileObserver: config.ReconcileObserver,
	}
	refreshScheduler := controller.NewRefreshScheduler(0)
	if err := manager.Add(refreshScheduler); err != nil {
		return nil, fmt.Errorf("add Refresh Scheduler: %w", err)
	}
	demandReconciler := &controller.NodeGroupDemandReconciler{
		Client: manager.GetClient(), Scheduler: refreshScheduler, Processor: processor,
	}
	if err := demandReconciler.SetupWithManager(manager); err != nil {
		return nil, fmt.Errorf("setup NodeGroupDemand controller: %w", err)
	}
	refreshReconciler := &controller.RefreshReconciler{
		Processor: processor, Scheduler: refreshScheduler,
		RefreshInterval: config.DemandRefreshInterval, MaxConcurrent: config.MaxConcurrentRefreshes,
	}
	if err := refreshReconciler.SetupWithManager(manager); err != nil {
		return nil, fmt.Errorf("setup NGD Refresh controller: %w", err)
	}
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return nil, fmt.Errorf("add PRC health check: %w", err)
	}
	if err := manager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return nil, fmt.Errorf("add PRC readiness check: %w", err)
	}
	return &Application{manager: manager, staticSnapshots: staticSnapshots}, nil
}

// Start blocks until ctx is cancelled or the Manager returns an error.
func (a *Application) Start(ctx context.Context) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("PRC application is not initialized")
	}
	return a.manager.Start(ctx)
}

// WaitForReady waits for the informer cache and for a static snapshot that the
// current Algorithm process has explicitly acknowledged.
func (a *Application) WaitForReady(ctx context.Context) (controller.StaticSnapshotStatus, error) {
	if a == nil || a.manager == nil || a.staticSnapshots == nil {
		return controller.StaticSnapshotStatus{}, fmt.Errorf("PRC application is not initialized")
	}
	if !a.manager.GetCache().WaitForCacheSync(ctx) {
		return controller.StaticSnapshotStatus{}, fmt.Errorf("wait for PRC cache sync: %w", ctx.Err())
	}
	return a.staticSnapshots.WaitForReady(ctx)
}

// StaticSnapshotStatus exposes read-only synchronization status for readiness
// checks and integration assertions without exposing the shared mutable state.
func (a *Application) StaticSnapshotStatus() (controller.StaticSnapshotStatus, bool) {
	if a == nil || a.staticSnapshots == nil {
		return controller.StaticSnapshotStatus{}, false
	}
	return a.staticSnapshots.Current()
}
