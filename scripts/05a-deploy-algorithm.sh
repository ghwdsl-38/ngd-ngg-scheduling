#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
kube apply -f "${ROOT_DIR}/config/manager/algorithm.yaml"
kube -n "${SYSTEM_NAMESPACE}" rollout restart deployment/ngd-ngg-algorithm
kube -n "${SYSTEM_NAMESPACE}" rollout status deployment/ngd-ngg-algorithm --timeout=120s
