#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
require_cmd docker
require_cmd kind
docker image inspect "${KUBE_SCHEDULER_IMAGE}" >/dev/null 2>&1 ||
  die "缺少镜像 ${KUBE_SCHEDULER_IMAGE}；请先运行 make kube-scheduler-image"
kind load docker-image "${KUBE_SCHEDULER_IMAGE}" --name "${CLUSTER_NAME}"
kube apply -f "${ROOT_DIR}/config/rbac/kube-scheduler-plugin.yaml"
kube apply -f "${ROOT_DIR}/config/kubescheduler/scheduler.yaml"
kube -n kube-system rollout restart deployment/ngg-scheduler
kube -n kube-system rollout status deployment/ngg-scheduler --timeout=180s
