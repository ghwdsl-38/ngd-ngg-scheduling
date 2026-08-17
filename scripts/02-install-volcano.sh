#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
require_cmd helm
require_cmd docker
require_cmd kind

VOLCANO_SOURCE="${ROOT_DIR}/../third_party/volcano"
CHART_DIR="${VOLCANO_SOURCE}/installer/helm/chart/volcano"
[[ -f "${CHART_DIR}/Chart.yaml" ]] || die "未找到本地Volcano Chart ${CHART_DIR}"

images=(
  "docker.io/volcanosh/vc-controller-manager:v1.15.0"
  "docker.io/volcanosh/vc-scheduler:v1.15.0"
  "docker.io/volcanosh/vc-webhook-manager:v1.15.0"
  "docker.io/library/busybox:1.36.1"
)
for image_name in "${images[@]}"; do
  if ! docker image inspect "${image_name}" >/dev/null 2>&1; then
    docker pull "${image_name}"
  fi
  kind load docker-image --name "${CLUSTER_NAME}" "${image_name}"
done

helm upgrade --install volcano "${CHART_DIR}" \
  --kube-context "${KUBE_CONTEXT}" \
  --namespace "${VOLCANO_NAMESPACE}" \
  --create-namespace \
  --set basic.image_pull_policy=IfNotPresent \
  --wait --timeout 10m
kube -n "${VOLCANO_NAMESPACE}" get pods -o wide

