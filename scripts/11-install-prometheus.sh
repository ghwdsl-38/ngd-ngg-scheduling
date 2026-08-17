#!/usr/bin/env bash

set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

MONITORING_DIR="${ROOT_DIR}/manifests/monitoring"
PROMETHEUS_IMAGE="quay.io/prometheus/prometheus:v3.13.0"
NODE_EXPORTER_IMAGE="quay.io/prometheus/node-exporter:v1.11.1"
KUBE_STATE_METRICS_IMAGE="registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.0"
KUBE_STATE_METRICS_MIRROR="swr.cn-north-4.myhuaweicloud.com/ddn-k8s/registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.0"
KUBE_STATE_METRICS_AMD64_ID="sha256:fa7117cd28fb5db10e5ff5d6e17c61b78a87ed0447e9028186850160f8c8f3fc"

ensure_cluster
require_cmd docker
require_cmd kind
docker info >/dev/null || die "Docker daemon 不可用"

monitoring_images=(
  "${PROMETHEUS_IMAGE}"
  "${NODE_EXPORTER_IMAGE}"
  "${KUBE_STATE_METRICS_IMAGE}"
)

for image in "${PROMETHEUS_IMAGE}" "${NODE_EXPORTER_IMAGE}"; do
  if ! docker image inspect "${image}" >/dev/null 2>&1; then
    log "拉取 ${image}"
    docker pull "${image}"
  fi
done

if ! docker image inspect "${KUBE_STATE_METRICS_IMAGE}" >/dev/null 2>&1; then
  log "拉取 ${KUBE_STATE_METRICS_IMAGE}"
  if ! docker pull "${KUBE_STATE_METRICS_IMAGE}"; then
    log "WARN: registry.k8s.io 不可达，改用已同步镜像并校验 amd64 镜像 ID"
    docker pull "${KUBE_STATE_METRICS_MIRROR}"
    mirror_id="$(docker image inspect "${KUBE_STATE_METRICS_MIRROR}" --format '{{.Id}}')"
    [[ "${mirror_id}" == "${KUBE_STATE_METRICS_AMD64_ID}" ]] ||
      die "kube-state-metrics 镜像 ID 不匹配：期望 ${KUBE_STATE_METRICS_AMD64_ID}，实际 ${mirror_id}"
    docker tag "${KUBE_STATE_METRICS_MIRROR}" "${KUBE_STATE_METRICS_IMAGE}"
  fi
fi

log "将监控镜像导入 Kind 集群 ${CLUSTER_NAME}"
kind load docker-image --name "${CLUSTER_NAME}" "${monitoring_images[@]}"

log "创建 monitoring 命名空间和轻量监控组件"
kube apply -f "${MONITORING_DIR}/namespace.yaml"
kube apply -f "${MONITORING_DIR}/node-exporter.yaml"
kube apply -f "${MONITORING_DIR}/kube-state-metrics.yaml"
kube apply -f "${MONITORING_DIR}/prometheus.yaml"

kube -n monitoring rollout status daemonset/node-exporter --timeout=180s
kube -n monitoring rollout status deployment/kube-state-metrics --timeout=180s
kube -n monitoring rollout status deployment/prometheus --timeout=180s
kube -n monitoring get pods -o wide

log "Prometheus 已安装；运行 scripts/12-check-prometheus.sh 验证采集结果"
