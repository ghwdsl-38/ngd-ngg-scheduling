#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

require_cmd docker
GO_MODULE_PROXY="${GO_MODULE_PROXY:-https://goproxy.cn,direct}"
docker run --rm \
  -e "GOPROXY=${GO_MODULE_PROXY}" \
  -e GOMODCACHE=/cache/mod \
  -e GOCACHE=/cache/build \
  -v "${ROOT_DIR}/plugin/kubescheduler:/workspace" \
  -v "${ROOT_DIR}/.cache/go-mod:/cache/mod" \
  -v "${ROOT_DIR}/.cache/go-build:/cache/build" \
  -w /workspace \
  golang:1.25.0 \
  sh -c 'gofmt -w nodegroupgrant/*.go && go test ./...' \
  2>&1 | tee "${RESULTS_DIR}/kube-scheduler-plugin-test.log"

