package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	prcapp "scheduling.demo.ngg.io/prc/pkg/application"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	defaultDemandRefreshInterval  = 15 * time.Second
	defaultMaxConcurrentRefreshes = 5
)

type refreshOptions struct {
	interval      time.Duration
	maxConcurrent int
}

func (o refreshOptions) validate() error {
	if o.interval <= 0 {
		return fmt.Errorf("--demand-refresh-interval must be greater than zero")
	}
	if o.maxConcurrent <= 0 {
		return fmt.Errorf("--max-concurrent-refreshes must be greater than zero")
	}
	return nil
}

func main() {
	var metricsAddr string
	var probeAddr string
	var leaderElection bool
	refresh := refreshOptions{}
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "Metrics endpoint bind address; 0 disables it")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Health probe bind address")
	flag.BoolVar(&leaderElection, "leader-elect", true, "Enable leader election")
	flag.DurationVar(&refresh.interval, "demand-refresh-interval", defaultDemandRefreshInterval, "normal delay between completed refreshes of one NGD")
	flag.IntVar(&refresh.maxConcurrent, "max-concurrent-refreshes", defaultMaxConcurrentRefreshes, "maximum number of NGDs refreshed concurrently")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	if err := refresh.validate(); err != nil {
		ctrl.Log.Error(err, "invalid PRC refresh configuration")
		os.Exit(2)
	}

	algorithmURL := env("ALGORITHM_URL", "http://ngd-ngg-algorithm.ngd-ngg-system.svc:8080")
	clusterID := env("CLUSTER_ID", "default-cluster")
	app, err := prcapp.New(prcapp.Config{
		KubernetesConfig: ctrl.GetConfigOrDie(), AlgorithmURL: algorithmURL, ClusterID: clusterID,
		MetricsBindAddress: metricsAddr, HealthProbeBindAddress: probeAddr, LeaderElection: leaderElection,
		DemandRefreshInterval: refresh.interval, MaxConcurrentRefreshes: refresh.maxConcurrent,
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
