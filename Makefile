SHELL := /usr/bin/env bash

.PHONY: check test algorithm-test algorithm-go-test algorithm-1000-test algorithm-integration-test algorithm-1000-demo kube-plugin-test load-images cluster volcano monitoring monitoring-check crds algorithm-image algorithm lldp-agent-image lldp-agent prc-image prc plugin-image plugin kube-scheduler-image kube-scheduler deploy deploy-prebuilt run run-kubernetes demo demo-prebuilt clean

check:
	./scripts/00-check-env.sh

test:
	PYTHONPATH=algorithm_api_server:src python3 -m unittest discover -s tests -v

algorithm-test:
	PYTHONPATH=algorithm_api_server python3 -m unittest discover -s tests -p 'test_algorithm*.py' -v

algorithm-go-test:
	docker run --rm -v "$(CURDIR)/algorithm_server:/workspace:ro" -w /workspace golang:1.25-alpine go test ./...

algorithm-1000-test:
	PYTHONPATH=algorithm_api_server python3 -m unittest discover -s tests -p 'test_algorithm_1000_nodes.py' -v

algorithm-integration-test:
	./scripts/10-test-algorithm-integration.sh

algorithm-1000-demo:
	docker build --tag "$${ALGORITHM_IMAGE:-ngd-ngg-algorithm:v0.4.0}" --file Dockerfile.algorithm .
	PYTHONPATH=algorithm_api_server python3 algorithm_api_server/demo_1000_nodes/run_demo.py

kube-plugin-test:
	./scripts/06b-test-kubescheduler-plugin.sh

load-images:
	./scripts/04c-load-images.sh

cluster:
	./scripts/01-create-kind.sh

volcano:
	./scripts/02-install-volcano.sh

monitoring:
	./scripts/11-install-prometheus.sh

monitoring-check:
	./scripts/12-check-prometheus.sh

crds:
	./scripts/03-install-apis.sh

prc-image:
	./scripts/04-build-prc.sh

algorithm-image:
	./scripts/04b-build-algorithm.sh

algorithm: algorithm-image
	./scripts/05a-deploy-algorithm.sh

lldp-agent-image:
	./scripts/04d-build-lldp-agent.sh

lldp-agent: lldp-agent-image
	./scripts/05b-deploy-lldp-agent.sh

prc: prc-image
	./scripts/05-deploy-prc.sh

plugin-image:
	./scripts/06-build-volcano-plugin.sh

plugin: plugin-image
	./scripts/07-deploy-volcano-plugin.sh

kube-scheduler-image:
	./scripts/06c-build-kubescheduler.sh

kube-scheduler: kube-scheduler-image
	./scripts/07b-deploy-kubescheduler.sh

deploy: crds lldp-agent algorithm prc plugin kube-scheduler

deploy-prebuilt: crds load-images
	./scripts/05b-deploy-lldp-agent.sh
	./scripts/05a-deploy-algorithm.sh
	./scripts/05-deploy-prc.sh
	./scripts/07-deploy-volcano-plugin.sh
	./scripts/07b-deploy-kubescheduler.sh

run:
	./scripts/08-run-demo.sh

run-kubernetes:
	./scripts/09-run-kubernetes-demo.sh

demo: check test cluster volcano monitoring deploy monitoring-check run run-kubernetes

demo-prebuilt: check test cluster volcano monitoring deploy-prebuilt monitoring-check run run-kubernetes

clean:
	./scripts/cleanup.sh
