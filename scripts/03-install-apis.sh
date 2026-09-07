#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
kube apply -f "${ROOT_DIR}/docs/paas-schedbridge-master-new/crd-deploy/nodegroupdemand-crd.yaml"
kube apply -f "${ROOT_DIR}/docs/paas-schedbridge-master-new/crd-deploy/nodegroupgrant-crd.yaml"
kube wait --for=condition=Established crd/nodegroupdemands.scheduling.platform.example.io --timeout=60s
kube wait --for=condition=Established crd/nodegroupgrants.scheduling.platform.example.io --timeout=60s
kube apply -f "${ROOT_DIR}/config/rbac/prc.yaml"
kube apply -f "${ROOT_DIR}/config/rbac/lldp-agent.yaml"
