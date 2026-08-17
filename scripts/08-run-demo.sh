#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
kube -n "${SYSTEM_NAMESPACE}" get deployment/ngd-ngg-algorithm >/dev/null 2>&1 ||
  die "Algorithm API Server未部署；请先运行make algorithm或make demo-prebuilt"
kube -n "${SYSTEM_NAMESPACE}" get service/ngd-ngg-algorithm >/dev/null 2>&1 ||
  die "Algorithm API Service未部署；请先运行make algorithm或make demo-prebuilt"
kube -n "${SYSTEM_NAMESPACE}" rollout status deployment/ngd-ngg-algorithm \
  --timeout=60s >/dev/null
algorithm_image="$(kube -n "${SYSTEM_NAMESPACE}" get deployment/ngd-ngg-algorithm \
  -o jsonpath='{.spec.template.spec.containers[0].image}')"
algorithm_ready="$(kube -n "${SYSTEM_NAMESPACE}" get deployment/ngd-ngg-algorithm \
  -o jsonpath='{.status.availableReplicas}')"
[[ "${algorithm_ready:-0}" -ge 1 ]] || die "Algorithm API Server没有可用副本"
log "Algorithm API Server已部署: image=${algorithm_image}, availableReplicas=${algorithm_ready}"

mapfile -t switch_c_nodes < <(
  kube get nodes -l 'topology.demo.ngg.io/switch=switch-c' \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort
)
(( ${#switch_c_nodes[@]} == 4 )) || die "switch-c应包含4个Worker"

cleanup_taint() {
  for node_name in "${switch_c_nodes[@]}"; do
    kube taint node "${node_name}" demo.ngg/blocked- >/dev/null 2>&1 || true
  done
}
trap cleanup_taint EXIT

kube -n "${DEMO_NAMESPACE}" delete jobs.batch.volcano.sh ngg-topology \
  --ignore-not-found --wait=true
kube -n "${DEMO_NAMESPACE}" delete nodegroupdemands.scheduling.demo.ngg.io topology \
  --ignore-not-found --wait=true

elapsed=0
while (( elapsed < 120 )); do
  remaining="$(kube -n "${DEMO_NAMESPACE}" get pods -l app=ngg-topology \
    --no-headers 2>/dev/null | awk 'END {print NR+0}')"
  [[ "${remaining}" -eq 0 ]] && break
  sleep 2
  elapsed=$((elapsed + 2))
done
[[ "${remaining:-0}" -eq 0 ]] || die "上一轮拓扑Demo Pod未清理完成"

# Algorithm第一版不处理Taint。预先阻塞首选switch-c，使第二层零绑定，
# 从而验证PRC在15秒后按rank切换到switch-a。
for node_name in "${switch_c_nodes[@]}"; do
  kube taint node "${node_name}" demo.ngg/blocked=true:NoSchedule --overwrite
done

kube apply -f "${ROOT_DIR}/manifests/topology-demand.yaml"
kube apply -f "${ROOT_DIR}/manifests/topology-job.yaml"

elapsed=0
while (( elapsed < 120 )); do
  group_count="$(kube -n "${DEMO_NAMESPACE}" get ngg ngg-topology \
    -o jsonpath='{.spec.candidateNodeGroups[*].groupId}' 2>/dev/null |
    awk '{print NF+0}' || true)"
  order="$(kube -n "${DEMO_NAMESPACE}" get ngg ngg-topology \
    -o jsonpath='{range .spec.candidateNodeGroups[*]}{.groupId}{" "}{end}' 2>/dev/null || true)"
  [[ "${group_count:-0}" -eq 3 && "${order}" == "switch-c switch-a switch-b " ]] && break
  sleep 2
  elapsed=$((elapsed + 2))
done
[[ "${group_count:-0}" -eq 3 ]] || die "Algorithm未返回3个候选组"
[[ "${order}" == "switch-c switch-a switch-b " ]] ||
  die "候选组顺序错误: ${order}"

algorithm_groups="$(kube -n "${DEMO_NAMESPACE}" get ngg ngg-topology \
  -o go-template='{{range .spec.candidateNodeGroups}}{{printf "rank=%v group=%v score=%v topology=%v nodes=%v\n" .rank .groupId .groupScore .topologyLevel (len .nodes)}}{{end}}')"
algorithm_versions="$(kube -n "${DEMO_NAMESPACE}" get ngg ngg-topology \
  -o go-template='algorithmBootId={{.spec.dataVersions.algorithmBootId}}{{"\n"}}nodeStaticSnapshotId={{.spec.dataVersions.nodeStaticSnapshotId}}{{"\n"}}schedulerStateSnapshotId={{.spec.dataVersions.schedulerStateSnapshotId}}{{"\n"}}metricSnapshotId={{.spec.dataVersions.metricSnapshotId}}{{"\n"}}')"
{
  printf 'Algorithm API Server部署信息\n'
  printf 'image=%s\n' "${algorithm_image}"
  printf 'availableReplicas=%s\n' "${algorithm_ready}"
  printf '\nAlgorithm计算并由PRC写入NGG的Top-3结果\n'
  printf '%s\n' "${algorithm_groups}"
  printf '\nAlgorithm本次计算使用的数据身份\n'
  printf '%s\n' "${algorithm_versions}"
} | tee "${RESULTS_DIR}/algorithm-calculation-result.txt"

elapsed=0
while (( elapsed < 180 )); do
  active_group="$(kube -n "${DEMO_NAMESPACE}" get ngg ngg-topology \
    -o jsonpath='{.spec.activeGroupRef.groupId}' 2>/dev/null || true)"
  active_rank="$(kube -n "${DEMO_NAMESPACE}" get ngg ngg-topology \
    -o jsonpath='{.spec.activeGroupRef.rank}' 2>/dev/null || true)"
  scheduled="$(kube -n "${DEMO_NAMESPACE}" get pods -l app=ngg-topology \
    -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' 2>/dev/null |
    awk 'NF {count++} END {print count+0}')"
  [[ "${active_group}" == "switch-a" && "${active_rank}" == "2" && "${scheduled}" -eq 4 ]] && break
  sleep 2
  elapsed=$((elapsed + 2))
done
[[ "${active_group}" == "switch-a" && "${active_rank}" == "2" ]] ||
  die "PRC未从switch-c切换到switch-a"
[[ "${scheduled:-0}" -eq 4 ]] || die "4个Pod未完成调度"

elapsed=0
while (( elapsed < 60 )); do
  active_state="$(kube -n "${DEMO_NAMESPACE}" get ngg ngg-topology \
    -o jsonpath='{.status.activeGroupState}' 2>/dev/null || true)"
  bound_pods="$(kube -n "${DEMO_NAMESPACE}" get ngg ngg-topology \
    -o jsonpath='{.status.boundPodCount}' 2>/dev/null || true)"
  [[ "${active_state}" == "Locked" && "${bound_pods:-0}" -eq 4 ]] && break
  sleep 2
  elapsed=$((elapsed + 2))
done
[[ "${active_state}" == "Locked" ]] || die "首个Pod绑定后NGG未进入Locked状态"
[[ "${bound_pods:-0}" -eq 4 ]] || die "NGG未记录全部4个已绑定Pod"

ngg_output_dir="${RESULTS_DIR}/generated-ngg-v2"
mkdir -p "${ngg_output_dir}"
kube -n "${DEMO_NAMESPACE}" get ngg ngg-topology -o yaml \
  >"${ngg_output_dir}/ngg-topology.yaml"
kube -n "${DEMO_NAMESPACE}" get ngd topology -o yaml \
  >"${ngg_output_dir}/ngd-topology.yaml"
kube get nnt -o yaml >"${ngg_output_dir}/node-network-topologies.yaml"

failed=0
placement_lines=()
while read -r pod node; do
  switch_id="$(kube get node "${node}" -o 'jsonpath={.metadata.labels.topology\.demo\.ngg\.io/switch}')"
  allowed="$(kube -n "${DEMO_NAMESPACE}" get ngg ngg-topology \
    -o jsonpath='{range .spec.candidateNodeGroups[1].nodes[*]}{.name}{"\n"}{end}' |
    awk -v target="${node}" '$0==target {print "yes"}')"
  printf -v line '%-36s node=%-38s switch=%-10s activeAllowed=%s' \
    "${pod}" "${node}" "${switch_id}" "${allowed:-no}"
  placement_lines+=("${line}")
  [[ "${switch_id}" == "switch-a" && "${allowed}" == "yes" ]] || failed=1
done < <(
  kube -n "${DEMO_NAMESPACE}" get pods -l app=ngg-topology \
    -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.nodeName}{"\n"}{end}'
)
printf '%s\n' "${placement_lines[@]}" | tee "${RESULTS_DIR}/topology-placement.txt"

{
  kube get nodes -L topology.demo.ngg.io/switch
  kube get nnt
  kube -n "${SYSTEM_NAMESPACE}" get daemonset/lldp-agent -o wide
  kube -n "${SYSTEM_NAMESPACE}" get pods -l app=ngd-ngg-lldp-agent -o wide
  kube -n "${DEMO_NAMESPACE}" get ngd,ngg -o wide
  kube -n "${DEMO_NAMESPACE}" get pods -o wide
} >"${RESULTS_DIR}/topology-cluster-state.txt"
kube -n "${SYSTEM_NAMESPACE}" logs deployment/ngd-ngg-algorithm --since=10m \
  >"${RESULTS_DIR}/algorithm.log"
kube -n "${SYSTEM_NAMESPACE}" logs deployment/prc --since=10m \
  >"${RESULTS_DIR}/prc-v2.log"
kube -n "${SYSTEM_NAMESPACE}" logs daemonset/lldp-agent \
  --all-pods=true --prefix --since=10m >"${RESULTS_DIR}/lldp-agent.log"

(( failed == 0 )) || die "Pod未落在当前activeGroup switch-a内"
log "Top-3顺序: ${order}"
log "实际切换: switch-c(rank=1) -> switch-a(rank=2)"
log "锁组状态: ${active_state}, boundPodCount=${bound_pods}"
log "NGD/NGG v2三交换机拓扑与候选组降级验证通过"
