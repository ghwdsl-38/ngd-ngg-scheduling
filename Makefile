SHELL := /usr/bin/env bash

.PHONY: check algorithm-1000-demo go-test-topology-agent go-test-group1 go-test-group2 go-test-group3 go-test-group4 go-test-group5 go-test-group7 go-test-all benchmark-3000-group1 benchmark-3000-group2 benchmark-3000-group3 benchmark-3000-group4 benchmark-3000-all benchmark-3000-report release-binaries release-images release-verify release-push crds algorithm-image algorithm lldp-agent-image lldp-agent prc-image prc deploy

check:
	./scripts/00-check-env.sh

algorithm-1000-demo:
	docker build --tag "$${ALGORITHM_IMAGE:-ngd-ngg-algorithm:v0.4.0}" --file Dockerfile.algorithm .
	PYTHONPATH=algorithm_server/python python3 algorithm_server/demo_1000_nodes/run_demo.py

go-test-topology-agent:
	. ./scripts/go-test-env.sh; cd topology_agent; GOWORK=off go test ./...

go-test-group1:
	. ./scripts/go-test-env.sh; go test -p=1 ./go_test_suites/group1_algorithm_worker -run '^TestGroup1_' -v -count=1

go-test-group2:
	. ./scripts/go-test-env.sh; go test -p=1 ./go_test_suites/group2_prc_algorithm -run '^TestGroup2_' -v -count=1

go-test-group3:
	. ./scripts/go-test-env.sh; go test -p=1 ./go_test_suites/group3_prc_ngd_ngg -run '^TestGroup3_' -v -count=1

go-test-group4:
	. ./scripts/go-test-env.sh; go test -p=1 ./go_test_suites/group4_full_real_algorithm -run '^TestGroup4_' -v -count=1

go-test-group5:
	. ./scripts/go-test-env.sh; go test -p=1 ./go_test_suites/group5_prc_refresh_lifecycle -run '^TestGroup5_' -v -count=1 -timeout=10m

go-test-group7:
	. ./scripts/go-test-env.sh; go test -p=1 ./go_test_suites/group7_bond_topology_flow -run '^TestGroup7_' -v -count=1 -timeout=10m

go-test-all: go-test-topology-agent go-test-group1 go-test-group2 go-test-group3 go-test-group4 go-test-group5 go-test-group7

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

release-binaries:
	RELEASE_VERSION="$${RELEASE_VERSION:-v0.6.1}" ./scripts/04-release-build-binaries.sh

release-images: release-binaries
	RELEASE_VERSION="$${RELEASE_VERSION:-v0.6.1}" ./scripts/04-release-package-images.sh

release-verify:
	RELEASE_VERSION="$${RELEASE_VERSION:-v0.6.1}" ./image_validation/verify.sh

release-push:
	RELEASE_VERSION="$${RELEASE_VERSION:-v0.6.1}" ./scripts/04-release-push-images.sh

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

deploy: crds lldp-agent algorithm prc
