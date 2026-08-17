#!/usr/bin/env bash

set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
kube -n monitoring rollout status daemonset/node-exporter --timeout=60s
kube -n monitoring rollout status deployment/kube-state-metrics --timeout=60s
kube -n monitoring rollout status deployment/prometheus --timeout=60s

proxy_base="/api/v1/namespaces/monitoring/services/http:prometheus:9090/proxy"
targets_file="${RESULTS_DIR}/prometheus-targets.json"
query_file="${RESULTS_DIR}/prometheus-query-checks.txt"
node_cpu_file="${RESULTS_DIR}/prometheus-node-cpu.json"
node_memory_file="${RESULTS_DIR}/prometheus-node-memory.json"

kube get --raw "${proxy_base}/api/v1/targets?state=active" >"${targets_file}"

query() {
  local name="$1" expression="$2" response value
  response="$(kube get --raw "${proxy_base}/api/v1/query?query=${expression}")"
  grep -q '"status":"success"' <<<"${response}" || die "Prometheus 查询失败: ${name}"
  value="$(sed -n 's/.*"value":\[[^,]*,"\([^"]*\)"\].*/\1/p' <<<"${response}")"
  [[ -n "${value}" ]] || die "Prometheus 查询没有返回标量结果: ${name}"
  printf '%s=%s\n' "${name}" "${value}"
}

{
  query healthy_targets 'count(up%20%3D%3D%201)'
  query node_exporter_nodes 'count(node_uname_info)'
  query kubernetes_nodes 'count(kube_node_info)'
  query cadvisor_series 'count(container_cpu_usage_seconds_total)'
} | tee "${query_file}"

kube get --raw "${proxy_base}/api/v1/query?query=1%20-%20avg%20by%20%28node%29%20%28rate%28node_cpu_seconds_total%7Bmode%3D%22idle%22%7D%5B5m%5D%29%29" \
  >"${node_cpu_file}"
kube get --raw "${proxy_base}/api/v1/query?query=1%20-%20%28node_memory_MemAvailable_bytes%20%2F%20node_memory_MemTotal_bytes%29" \
  >"${node_memory_file}"
grep -q '"status":"success"' "${node_cpu_file}" || die "Algorithm CPU PromQL查询失败"
grep -q '"status":"success"' "${node_memory_file}" || die "Algorithm内存PromQL查询失败"
grep -q '"result":\[{' "${node_cpu_file}" || die "Algorithm CPU PromQL结果为空"
grep -q '"result":\[{' "${node_memory_file}" || die "Algorithm内存PromQL结果为空"

node_exporter_nodes="$(sed -n 's/^node_exporter_nodes=//p' "${query_file}")"
kubernetes_nodes="$(sed -n 's/^kubernetes_nodes=//p' "${query_file}")"
[[ "${node_exporter_nodes}" == "9" ]] || die "预期 9 个Worker的node-exporter指标，实际 ${node_exporter_nodes}"
[[ "${kubernetes_nodes}" == "10" ]] || die "预期 10 个Kubernetes Node，实际 ${kubernetes_nodes}"

kube -n monitoring get pods -o wide | tee "${RESULTS_DIR}/monitoring-pods.txt"

algorithm_proxy="/api/v1/namespaces/${SYSTEM_NAMESPACE}/services/http:ngd-ngg-algorithm:8080/proxy"
algorithm_cache_file="${RESULTS_DIR}/algorithm-metrics-cache-status.json"
elapsed=0
while (( elapsed < 90 )); do
  algorithm_cache="$(kube get --raw "${algorithm_proxy}/internal/v1/cache/status" 2>/dev/null || true)"
  if grep -q '"metrics":{"ready":true,"enabled":true,"degraded":false' <<<"${algorithm_cache}" &&
    grep -q '"nodeCount":9' <<<"${algorithm_cache}"; then
    break
  fi
  sleep 2
  elapsed=$((elapsed + 2))
done
printf '%s\n' "${algorithm_cache}" >"${algorithm_cache_file}"
grep -q '"metrics":{"ready":true,"enabled":true,"degraded":false' "${algorithm_cache_file}" ||
  die "Algorithm的Prometheus指标缓存未就绪"
grep -q '"nodeCount":9' "${algorithm_cache_file}" ||
  die "Algorithm指标缓存未包含9个Worker"

log "Prometheus和Algorithm缓存验证通过: workerMetrics=9"
log "检查结果、节点指标和Algorithm缓存状态位于 ${RESULTS_DIR}"
