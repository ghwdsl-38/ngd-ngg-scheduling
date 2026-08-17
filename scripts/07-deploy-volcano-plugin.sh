#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
require_cmd docker
docker image inspect "${SCHEDULER_IMAGE}" >/dev/null 2>&1 ||
  die "缺少镜像 ${SCHEDULER_IMAGE}；请先运行make plugin-image"
kind load docker-image "${SCHEDULER_IMAGE}" --name "${CLUSTER_NAME}"
kube apply -f "${ROOT_DIR}/config/rbac/volcano-plugin.yaml"
kube apply -f "${ROOT_DIR}/config/volcano/scheduler-config.yaml"
kube -n "${VOLCANO_NAMESPACE}" set image deployment/volcano-scheduler \
  volcano-scheduler="${SCHEDULER_IMAGE}"
kube -n "${VOLCANO_NAMESPACE}" patch deployment volcano-scheduler --type=strategic \
  -p='{"spec":{"template":{"spec":{"containers":[{"name":"volcano-scheduler","imagePullPolicy":"IfNotPresent"}]}}}}'
args="$(kube -n "${VOLCANO_NAMESPACE}" get deployment volcano-scheduler \
  -o jsonpath='{range .spec.template.spec.containers[0].args[*]}{.}{"\n"}{end}')"
if ! grep -Fxq -- '-v=4' <<<"${args}"; then
  kube -n "${VOLCANO_NAMESPACE}" patch deployment volcano-scheduler --type=json \
    -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"-v=4"}]'
fi
kube -n "${VOLCANO_NAMESPACE}" rollout status deployment/volcano-scheduler --timeout=180s
kube -n "${VOLCANO_NAMESPACE}" get deployment volcano-scheduler \
  -o custom-columns='NAME:.metadata.name,IMAGE:.spec.template.spec.containers[0].image'

