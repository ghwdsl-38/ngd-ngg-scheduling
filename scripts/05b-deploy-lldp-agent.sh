#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
kube apply -f "${ROOT_DIR}/config/rbac/lldp-agent.yaml"
kube apply -f "${ROOT_DIR}/config/manager/lldp-agent.yaml"
kube -n "${SYSTEM_NAMESPACE}" rollout restart daemonset/lldp-agent
kube -n "${SYSTEM_NAMESPACE}" rollout status daemonset/lldp-agent --timeout=180s

kube -n "${SYSTEM_NAMESPACE}" get daemonset/lldp-agent
kube get nodes -L topology.demo.ngg.io/leaf-switch,topology.demo.ngg.io/leaf-set-id,topology.demo.ngg.io/leaf-count
