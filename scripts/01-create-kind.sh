#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

require_cmd kind
require_cmd docker
KIND_NODE_IMAGE="kindest/node:v1.35.5@sha256:ce977ae6d65918d0b58a5f8b5e940429c2ce42fa3a5619ec2bbc60b949c0ac95"
if kind get clusters 2>/dev/null | grep -Fxq "${CLUSTER_NAME}"; then
  log "Kind集群 ${CLUSTER_NAME} 已存在"
else
  kind create cluster \
    --name "${CLUSTER_NAME}" \
    --config "${ROOT_DIR}/kind/kind-config.yaml" \
    --image "${KIND_NODE_IMAGE}" \
    --wait 180s
fi
kube wait --for=condition=Ready nodes --all --timeout=180s
worker_count="$(kube get nodes -l '!node-role.kubernetes.io/control-plane' --no-headers | awk 'END {print NR+0}')"
[[ "${worker_count}" -eq 9 ]] ||
  die "v2拓扑Demo需要9个Worker，当前集群 ${CLUSTER_NAME} 有 ${worker_count} 个；请更换集群名或清理旧集群"
kube get nodes -o wide
