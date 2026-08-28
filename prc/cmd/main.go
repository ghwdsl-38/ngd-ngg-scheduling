package main

import (
	"flag"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"scheduling.demo.ngg.io/prc/pkg/controller"
)

func main() {
	var metricsAddr string
	var probeAddr string
	var leaderElection bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "Metrics endpoint bind address; 0 disables it")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Health probe bind address")
	flag.BoolVar(&leaderElection, "leader-elect", true, "Enable leader election")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElection,
		LeaderElectionID:       "ngd-ngg-prc.scheduling.demo.ngg.io",
	})
	if err != nil {
		ctrl.Log.Error(err, "create controller manager")
		os.Exit(1)
	}

	algorithmURL := env("ALGORITHM_URL", "http://ngd-ngg-algorithm.ngd-ngg-system.svc:8080")
	clusterID := env("CLUSTER_ID", "volcano-ngd-ngg-v2-demo")
	staticSnapshots := controller.NewStaticSnapshotState()
	staticReconciler := &controller.NodeStaticSnapshotReconciler{
		Client: mgr.GetClient(), AlgorithmURL: algorithmURL, ClusterID: clusterID, State: staticSnapshots,
	}
	if err := staticReconciler.SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "setup Node static snapshot controller")
		os.Exit(1)
	}

	reconciler := &controller.NodeGroupDemandReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		AlgorithmURL:    algorithmURL,
		ClusterID:       clusterID,
		StaticSnapshots: staticSnapshots,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "setup NodeGroupDemand controller")
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		ctrl.Log.Error(err, "add health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		ctrl.Log.Error(err, "add readiness check")
		os.Exit(1)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		ctrl.Log.Error(err, "run controller manager")
		os.Exit(1)
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
