#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOLS_DIR="${ROOT_DIR}/../volcano-scheduling-demo/.tools"
RESULTS_DIR="${ROOT_DIR}/results"
CLUSTER_NAME="${CLUSTER_NAME:-volcano-ngd-ngg-v2-demo}"
KUBE_CONTEXT="${KUBE_CONTEXT:-kind-${CLUSTER_NAME}}"
VOLCANO_NAMESPACE="${VOLCANO_NAMESPACE:-volcano-system}"
SYSTEM_NAMESPACE="${SYSTEM_NAMESPACE:-ngd-ngg-system}"
DEMO_NAMESPACE="${DEMO_NAMESPACE:-ngd-ngg-demo}"
PRC_IMAGE="${PRC_IMAGE:-ngd-ngg-prc:v0.3.0}"
ALGORITHM_IMAGE="${ALGORITHM_IMAGE:-ngd-ngg-algorithm:v0.4.0}"
LLDP_AGENT_IMAGE="${LLDP_AGENT_IMAGE:-ngd-ngg-lldp-agent:v0.1.0}"
SCHEDULER_IMAGE="${SCHEDULER_IMAGE:-volcano-ngg-scheduler:v1.15.0}"
KUBE_SCHEDULER_IMAGE="${KUBE_SCHEDULER_IMAGE:-ngg-kube-scheduler:v1.35.3}"
IMAGES_DIR="${ROOT_DIR}/images"

export PATH="${TOOLS_DIR}/bin:${PATH}"
mkdir -p "${RESULTS_DIR}" "${IMAGES_DIR}"

log() {
  printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"
}

die() {
  log "ERROR: $*" >&2
  exit 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令 $1"
}

kube() {
  kubectl --context "${KUBE_CONTEXT}" "$@"
}

ensure_cluster() {
  require_cmd kubectl
  kubectl config get-contexts "${KUBE_CONTEXT}" >/dev/null 2>&1 ||
    die "未找到上下文 ${KUBE_CONTEXT}；请先运行 make cluster"
  kube cluster-info >/dev/null
}
