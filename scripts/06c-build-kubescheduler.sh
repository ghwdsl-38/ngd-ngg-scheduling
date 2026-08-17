#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

require_cmd docker
docker info >/dev/null || die "Docker daemon不可用"
mkdir -p "${ROOT_DIR}/build" "${ROOT_DIR}/.cache/go-mod" "${ROOT_DIR}/.cache/go-build"
docker run --rm \
  -e GOMODCACHE=/cache/mod \
  -e GOCACHE=/cache/build \
  -e CGO_ENABLED=0 \
  -v "${ROOT_DIR}/plugin/kubescheduler:/workspace" \
  -v "${ROOT_DIR}/build:/out" \
  -v "${ROOT_DIR}/.cache/go-mod:/cache/mod" \
  -v "${ROOT_DIR}/.cache/go-build:/cache/build" \
  -w /workspace \
  golang:1.25.0 \
  sh -c 'go test ./... && go build -trimpath -ldflags="-s -w" -o /out/ngg-scheduler ./cmd/ngg-scheduler'
docker build --tag "${KUBE_SCHEDULER_IMAGE}" --file "${ROOT_DIR}/Dockerfile.kubescheduler" "${ROOT_DIR}"
docker image inspect "${KUBE_SCHEDULER_IMAGE}" --format '{{.Id}} {{.RepoTags}}' |
  tee "${RESULTS_DIR}/kube-scheduler-image.txt"
docker save "${KUBE_SCHEDULER_IMAGE}" -o "${IMAGES_DIR}/ngg-kube-scheduler-v1.35.3.tar"
chmod 0644 "${IMAGES_DIR}/ngg-kube-scheduler-v1.35.3.tar"
if kind get clusters 2>/dev/null | grep -Fxq "${CLUSTER_NAME}"; then
  kind load docker-image "${KUBE_SCHEDULER_IMAGE}" --name "${CLUSTER_NAME}"
fi
