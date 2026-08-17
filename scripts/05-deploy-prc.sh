#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
require_cmd docker
require_cmd kind
docker image inspect "${PRC_IMAGE}" >/dev/null 2>&1 ||
  die "缺少镜像 ${PRC_IMAGE}；请先运行make prc-image或make load-images"
kind load docker-image "${PRC_IMAGE}" --name "${CLUSTER_NAME}"
kube apply -f "${ROOT_DIR}/config/rbac/prc.yaml"
kube -n "${SYSTEM_NAMESPACE}" get deployment/ngd-ngg-algorithm >/dev/null 2>&1 ||
  die "Algorithm API Server尚未部署；请先运行make algorithm"
kube apply -f "${ROOT_DIR}/config/manager/prc.yaml"
kube -n "${SYSTEM_NAMESPACE}" rollout restart deployment/prc
kube -n "${SYSTEM_NAMESPACE}" rollout status deployment/prc --timeout=120s
