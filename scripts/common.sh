#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOLS_DIR="${ROOT_DIR}/../.tools"
RESULTS_DIR="${ROOT_DIR}/results"
KUBE_CONTEXT="${KUBE_CONTEXT:-}"
SYSTEM_NAMESPACE="${SYSTEM_NAMESPACE:-ngd-ngg-system}"
PRC_IMAGE="${PRC_IMAGE:-ngd-ngg-prc:v0.3.0}"
ALGORITHM_IMAGE="${ALGORITHM_IMAGE:-ngd-ngg-algorithm:v0.4.0}"
LLDP_AGENT_IMAGE="${LLDP_AGENT_IMAGE:-ngd-ngg-lldp-agent:v0.2.0}"
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
  if [[ -n "${KUBE_CONTEXT}" ]]; then
    kubectl --context "${KUBE_CONTEXT}" "$@"
  else
    kubectl "$@"
  fi
}

ensure_cluster() {
  require_cmd kubectl
  if [[ -n "${KUBE_CONTEXT}" ]]; then
    kubectl config get-contexts "${KUBE_CONTEXT}" >/dev/null 2>&1 ||
      die "未找到Kubernetes上下文 ${KUBE_CONTEXT}"
  fi
  kube cluster-info >/dev/null
}
