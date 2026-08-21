#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
kube apply -f "${ROOT_DIR}/config/crd/nodegroupdemand.yaml"
kube apply -f "${ROOT_DIR}/docs/paas-schedbridge-master/crd-deploy/nodegroupdemand-crd.yaml"
kube apply -f "${ROOT_DIR}/config/crd/nodegroupgrant.yaml"
kube apply -f "${ROOT_DIR}/config/crd/nodegroupgrant-platform.yaml"
kube apply -f "${ROOT_DIR}/config/crd/nodenetworktopology.yaml"
kube wait --for=condition=Established crd/nodegroupdemands.scheduling.demo.ngg.io --timeout=60s
kube wait --for=condition=Established crd/nodegroupdemands.scheduling.platform.example.io --timeout=60s
kube wait --for=condition=Established crd/nodegroupgrants.scheduling.demo.ngg.io --timeout=60s
kube wait --for=condition=Established crd/nodegroupgrants.scheduling.platform.example.io --timeout=60s
kube wait --for=condition=Established crd/nodenetworktopologies.scheduling.demo.ngg.io --timeout=60s
kube apply -f "${ROOT_DIR}/config/rbac/prc.yaml"
kube apply -f "${ROOT_DIR}/config/rbac/volcano-plugin.yaml"
kube apply -f "${ROOT_DIR}/manifests/demo-resources.yaml"

mapfile -t workers < <(
  kube get nodes -l '!node-role.kubernetes.io/control-plane' \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort
)
(( ${#workers[@]} == 9 )) || die "需要9个Worker，当前有 ${#workers[@]} 个"
"${SCRIPT_DIR}/03a-configure-topology.sh"
