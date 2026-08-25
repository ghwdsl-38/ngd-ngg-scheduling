SHELL := /usr/bin/env bash

.PHONY: check algorithm-1000-demo go-test-group1 go-test-group2 go-test-group3 go-test-group4 go-test-all benchmark-3000-group1 benchmark-3000-group2 benchmark-3000-group3 benchmark-3000-group4 benchmark-3000-all benchmark-3000-report demo-group1-timing demo-group1-evidence demo-group1 demo-group2-timing demo-group2-evidence demo-group2 demo-group3-timing demo-group3-evidence demo-group3 demo-all-groups demo-show-latest test-algorithm-complete test-prc-complete test-full-chain-simulated test-acceptance-v2 test-showcase load-images cluster volcano monitoring monitoring-check crds algorithm-image algorithm lldp-agent-image lldp-agent prc-image prc plugin-image plugin kube-scheduler-image kube-scheduler deploy deploy-prebuilt run run-kubernetes demo demo-prebuilt clean

check:
	./scripts/00-check-env.sh

algorithm-1000-demo:
	docker build --tag "$${ALGORITHM_IMAGE:-ngd-ngg-algorithm:v0.4.0}" --file Dockerfile.algorithm .
	PYTHONPATH=algorithm_server/python python3 algorithm_server/demo_1000_nodes/run_demo.py

go-test-group1:
	. ./scripts/go-test-env.sh; go test -p=1 ./go_test_suites/group1_algorithm_worker -run '^TestGroup1_' -v -count=1

go-test-group2:
	. ./scripts/go-test-env.sh; go test -p=1 ./go_test_suites/group2_prc_algorithm -run '^TestGroup2_' -v -count=1

go-test-group3:
	. ./scripts/go-test-env.sh; go test -p=1 ./go_test_suites/group3_prc_ngd_ngg -run '^TestGroup3_' -v -count=1

go-test-group4:
	. ./scripts/go-test-env.sh; go test -p=1 ./go_test_suites/group4_full_real_algorithm -run '^TestGroup4_' -v -count=1

go-test-all: go-test-group1 go-test-group2 go-test-group3 go-test-group4

benchmark-3000-group1:
	. ./scripts/go-test-env.sh; go test -p=1 ./go_test_suites/scale_benchmark_3000/group1_algorithm_worker -run '^TestGroup1Scale3000$$' -v -count=1 -timeout=30m

benchmark-3000-group2:
	. ./scripts/go-test-env.sh; go test -p=1 ./go_test_suites/scale_benchmark_3000/group2_prc_algorithm -run '^TestGroup2Scale3000$$' -v -count=1 -timeout=30m

benchmark-3000-group3:
	. ./scripts/go-test-env.sh; go test -p=1 ./go_test_suites/scale_benchmark_3000/group3_prc_ngd_ngg -run '^TestGroup3Scale3000$$' -v -count=1 -timeout=30m

benchmark-3000-group4:
	. ./scripts/go-test-env.sh; go test -p=1 ./go_test_suites/scale_benchmark_3000/group4_full_real_algorithm -run '^TestGroup4Scale3000$$' -v -count=1 -timeout=30m

benchmark-3000-all:
	./scripts/run-3000-scale-benchmark.sh

benchmark-3000-report:
	. ./scripts/go-test-env.sh; go run ./go_test_suites/scale_benchmark_3000/cmd/report

demo-group1-timing: algorithm-image
	./test_suites/group1_algorithm/run-group.sh --mode timing

demo-group1-evidence: algorithm-image
	./test_suites/group1_algorithm/run-group.sh --mode evidence

demo-group1: algorithm-image
	./test_suites/group1_algorithm/run-group.sh --mode all

demo-group2-timing:
	./test_suites/group2_prc/run-group.sh --mode timing

demo-group2-evidence:
	./test_suites/group2_prc/run-group.sh --mode evidence

demo-group2:
	./test_suites/group2_prc/run-group.sh --mode all

demo-group3-timing: algorithm-image
	./test_suites/group3_full_chain/run-group.sh --mode timing

demo-group3-evidence: algorithm-image
	./test_suites/group3_full_chain/run-group.sh --mode evidence

demo-group3: algorithm-image
	./test_suites/group3_full_chain/run-group.sh --mode all

demo-all-groups: demo-group1 demo-group2 demo-group3

demo-show-latest:
	./test_suites/show-latest.sh

test-algorithm-complete: demo-group1

test-prc-complete: demo-group2

test-full-chain-simulated: demo-group3

test-acceptance-v2: test-algorithm-complete test-prc-complete test-full-chain-simulated

test-showcase: demo-show-latest

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

demo: check cluster volcano monitoring deploy monitoring-check run run-kubernetes

demo-prebuilt: check cluster volcano monitoring deploy-prebuilt monitoring-check run run-kubernetes

clean:
	./scripts/cleanup.sh
