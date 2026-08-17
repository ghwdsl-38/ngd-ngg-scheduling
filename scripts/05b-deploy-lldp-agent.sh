#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
require_cmd docker
require_cmd kind
docker image inspect "${LLDP_AGENT_IMAGE}" >/dev/null 2>&1 ||
  die "缺少镜像 ${LLDP_AGENT_IMAGE}；请先运行make lldp-agent-image或make load-images"
kind load docker-image "${LLDP_AGENT_IMAGE}" --name "${CLUSTER_NAME}"
kube apply -f "${ROOT_DIR}/config/rbac/lldp-agent.yaml"
kube apply -f "${ROOT_DIR}/config/manager/lldp-agent.yaml"
kube -n "${SYSTEM_NAMESPACE}" rollout restart daemonset/lldp-agent
kube -n "${SYSTEM_NAMESPACE}" rollout status daemonset/lldp-agent --timeout=180s

elapsed=0
while (( elapsed < 120 )); do
  ready_count="$(kube get nnt \
    -o jsonpath='{range .items[?(@.status.source=="SimulatedLLDP")]}{.metadata.name}{"\n"}{end}' \
    2>/dev/null | awk 'NF {count++} END {print count+0}')"
  [[ "${ready_count}" -eq 9 ]] && break
  sleep 2
  elapsed=$((elapsed + 2))
done
[[ "${ready_count:-0}" -eq 9 ]] ||
  die "LLDP Agent未生成9个可用NodeNetworkTopology，当前 ${ready_count:-0} 个"
kube -n "${SYSTEM_NAMESPACE}" get daemonset/lldp-agent
kube get nnt
