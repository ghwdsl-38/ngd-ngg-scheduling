package main

import (
	"os"

	"k8s.io/component-base/cli"
	_ "k8s.io/component-base/logs/json/register"
	_ "k8s.io/component-base/metrics/prometheus/clientgo"
	_ "k8s.io/component-base/metrics/prometheus/version"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"

	"scheduling.demo.ngg.io/kubescheduler-plugin/nodegroupgrant"
)

func main() {
	command := app.NewSchedulerCommand(
		app.WithPlugin(nodegroupgrant.Name, nodegroupgrant.New),
	)
	os.Exit(cli.Run(command))
}
