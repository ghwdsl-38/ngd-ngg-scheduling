package main

import (
	"flag"
	"os"

	prcapp "scheduling.demo.ngg.io/prc/pkg/application"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
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

	algorithmURL := env("ALGORITHM_URL", "http://ngd-ngg-algorithm.ngd-ngg-system.svc:8080")
	clusterID := env("CLUSTER_ID", "default-cluster")
	app, err := prcapp.New(prcapp.Config{
		KubernetesConfig: ctrl.GetConfigOrDie(), AlgorithmURL: algorithmURL, ClusterID: clusterID,
		MetricsBindAddress: metricsAddr, HealthProbeBindAddress: probeAddr, LeaderElection: leaderElection,
	})
	if err != nil {
		ctrl.Log.Error(err, "create PRC application")
		os.Exit(1)
	}
	if err := app.Start(ctrl.SetupSignalHandler()); err != nil {
		ctrl.Log.Error(err, "run PRC application")
		os.Exit(1)
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
