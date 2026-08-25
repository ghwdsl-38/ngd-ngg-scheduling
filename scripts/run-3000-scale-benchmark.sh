#!/usr/bin/env bash
set -euo pipefail

project_root="/mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo"
cd "$project_root"
source scripts/go-test-env.sh

run_id="${BENCHMARK_RUN_ID:-$(date +%Y%m%d-%H%M%S)}"
export BENCHMARK_RUN_ID="$run_id"
export BENCHMARK_TARGETS="${BENCHMARK_TARGETS:-1000,800,500,300,100,10}"
export BENCHMARK_SAMPLES="${BENCHMARK_SAMPLES:-30}"
export BENCHMARK_WARMUPS="${BENCHMARK_WARMUPS:-3}"

go test -p=1 ./go_test_suites/scale_benchmark_3000/group1_algorithm_worker -run '^TestGroup1Scale3000$' -v -count=1 -timeout=30m
go test -p=1 ./go_test_suites/scale_benchmark_3000/group2_prc_algorithm -run '^TestGroup2Scale3000$' -v -count=1 -timeout=30m
go test -p=1 ./go_test_suites/scale_benchmark_3000/group3_prc_ngd_ngg -run '^TestGroup3Scale3000$' -v -count=1 -timeout=30m
go test -p=1 ./go_test_suites/scale_benchmark_3000/group4_full_real_algorithm -run '^TestGroup4Scale3000$' -v -count=1 -timeout=30m
go run ./go_test_suites/scale_benchmark_3000/cmd/report

echo "BENCHMARK_RUN_ID=$run_id"
