#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
kube -n kube-system get deployment/ngg-scheduler >/dev/null 2>&1 ||
  die "ngg-scheduler 尚未部署；请先运行 make kube-scheduler"
kube apply -f "${ROOT_DIR}/config/crd/nodegroupdemand.yaml"
kube apply -f "${ROOT_DIR}/config/crd/nodegroupgrant.yaml"

kube -n "${DEMO_NAMESPACE}" delete job/ngg-kubernetes ngd/kubernetes ngg/ngg-kubernetes --ignore-not-found --wait=true
for _ in $(seq 1 120); do
  remaining="$(kube -n "${DEMO_NAMESPACE}" get pod -l job-name=ngg-kubernetes --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  [[ "${remaining}" -eq 0 ]] && break
  sleep 1
done
[[ "${remaining:-0}" -eq 0 ]] || die "上一轮 Kubernetes Job Pod 未清理完成"
kube apply -f "${ROOT_DIR}/manifests/kubernetes-job.yaml"

log "先不给任务创建 NGD，验证缺少有效 NGG 时故障关闭"
for _ in $(seq 1 30); do
  count="$(kube -n "${DEMO_NAMESPACE}" get pod -l job-name=ngg-kubernetes --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  [[ "${count}" -ge 4 ]] && break
  sleep 1
done
sleep 3
if kube -n "${DEMO_NAMESPACE}" get pod -l job-name=ngg-kubernetes -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | grep -q '[^[:space:]]'; then
  die "缺少 NGG 时已有 Kubernetes Job Pod 被绑定，Fail Closed 验证失败"
fi
kube -n "${DEMO_NAMESPACE}" get pod -l job-name=ngg-kubernetes -o wide |
  tee "${RESULTS_DIR}/kubernetes-fail-closed.txt"

kube apply -f "${ROOT_DIR}/manifests/kubernetes-demand.yaml"
for _ in $(seq 1 120); do
  phase="$(kube -n "${DEMO_NAMESPACE}" get ngg/ngg-kubernetes -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  state="$(kube -n "${DEMO_NAMESPACE}" get ngg/ngg-kubernetes -o jsonpath='{.status.activeGroupState}' 2>/dev/null || true)"
  [[ "${phase}" == "Active" && ( "${state}" == "Trying" || "${state}" == "Locked" ) ]] && break
  sleep 1
done
[[ "${phase:-}" == "Active" ]] || die "等待 Kubernetes Job NGG 激活超时"

for _ in $(seq 1 120); do
  bound="$(kube -n "${DEMO_NAMESPACE}" get pod -l job-name=ngg-kubernetes -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | grep -c '[^[:space:]]' || true)"
  [[ "${bound}" -eq 4 ]] && break
  sleep 1
done
[[ "${bound:-0}" -eq 4 ]] || die "等待 Kubernetes Job 的 4 个 Pod 绑定超时"

for _ in $(seq 1 60); do
  locked_state="$(kube -n "${DEMO_NAMESPACE}" get ngg/ngg-kubernetes -o jsonpath='{.status.activeGroupState}' 2>/dev/null || true)"
  recorded_bound="$(kube -n "${DEMO_NAMESPACE}" get ngg/ngg-kubernetes -o jsonpath='{.status.boundPodCount}' 2>/dev/null || true)"
  [[ "${locked_state}" == "Locked" && "${recorded_bound:-0}" -eq 4 ]] && break
  sleep 1
done
[[ "${locked_state:-}" == "Locked" && "${recorded_bound:-0}" -eq 4 ]] ||
  die "Pod 已绑定，但 PRC 未在 NGG 中完成 Locked/boundPodCount=4 状态确认"

kube -n "${DEMO_NAMESPACE}" get ngd/kubernetes -o yaml >"${RESULTS_DIR}/generated-ngg-v2/ngd-kubernetes.yaml"
kube -n "${DEMO_NAMESPACE}" get ngg/ngg-kubernetes -o yaml >"${RESULTS_DIR}/generated-ngg-v2/ngg-kubernetes.yaml"
kube -n "${DEMO_NAMESPACE}" get pod -l job-name=ngg-kubernetes -o wide |
  tee "${RESULTS_DIR}/kubernetes-placement.txt"
kube -n kube-system logs deployment/ngg-scheduler --tail=300 >"${RESULTS_DIR}/kube-scheduler.log"

active_group="$(kube -n "${DEMO_NAMESPACE}" get ngg/ngg-kubernetes -o jsonpath='{.spec.activeGroupRef.groupId}')"
active_nodes="$(kube -n "${DEMO_NAMESPACE}" get ngg/ngg-kubernetes -o jsonpath="{range .spec.candidateNodeGroups[?(@.groupId=='${active_group}')].nodes[*]}{.name}{'\\n'}{end}")"
while IFS= read -r node; do
  [[ -z "${node}" ]] && continue
  grep -Fxq "${node}" <<<"${active_nodes}" || die "Pod 被绑定到 activeGroup 之外的 Node: ${node}"
done < <(kube -n "${DEMO_NAMESPACE}" get pod -l job-name=ngg-kubernetes -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}')
log "Kubernetes Job + 自定义 kube-scheduler NGG 插件演示通过"
