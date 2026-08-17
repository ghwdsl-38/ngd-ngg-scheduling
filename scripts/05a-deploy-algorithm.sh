#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
require_cmd docker
docker image inspect "${ALGORITHM_IMAGE}" >/dev/null 2>&1 ||
  die "缺少镜像 ${ALGORITHM_IMAGE}；请先运行make algorithm-image"
kind load docker-image "${ALGORITHM_IMAGE}" --name "${CLUSTER_NAME}"
kube apply -f "${ROOT_DIR}/config/manager/algorithm.yaml"
kube -n "${SYSTEM_NAMESPACE}" rollout restart deployment/ngd-ngg-algorithm
kube -n "${SYSTEM_NAMESPACE}" rollout status deployment/ngd-ngg-algorithm --timeout=120s
