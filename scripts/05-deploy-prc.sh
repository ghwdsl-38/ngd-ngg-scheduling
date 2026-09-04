#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
kube apply -f "${ROOT_DIR}/config/rbac/prc.yaml"
kube -n "${SYSTEM_NAMESPACE}" get deployment/ngd-ngg-algorithm >/dev/null 2>&1 ||
  die "Algorithm API Server尚未部署；请先运行make algorithm"
kube apply -f "${ROOT_DIR}/config/manager/prc.yaml"
kube -n "${SYSTEM_NAMESPACE}" rollout restart deployment/prc
kube -n "${SYSTEM_NAMESPACE}" rollout status deployment/prc --timeout=120s
