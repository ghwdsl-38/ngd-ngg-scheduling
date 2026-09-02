#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

ensure_cluster
mapfile -t workers < <(
  kube get nodes -l '!node-role.kubernetes.io/control-plane' \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort
)
(( ${#workers[@]} == 9 )) || die "三交换机拓扑需要9个Worker"

for index in "${!workers[@]}"; do
  ordinal=$((index + 1))
  if (( ordinal <= 3 )); then
    switch_id="switch-a"
  elif (( ordinal <= 5 )); then
    switch_id="switch-b"
  else
    switch_id="switch-c"
  fi
  node_name="${workers[index]}"
  kube label node "${node_name}" \
    demo.ngg/worker=true \
    --overwrite
  kube annotate node "${node_name}" \
    topology.demo.ngg.io/simulated-leaf-switch="${switch_id}" \
    topology.demo.ngg.io/local-interface=eth0 \
    topology.demo.ngg.io/remote-port="Ethernet1/${ordinal}" --overwrite
  # 旧版Agent曾把上层拓扑和链路指标写入Node；新模型只持久化直接Leaf。
  kube label node "${node_name}" \
    topology.demo.ngg.io/core-switch- \
    topology.demo.ngg.io/border-switch- \
    topology.demo.ngg.io/convergence-switch- \
    topology.demo.ngg.io/bandwidth-gbps- \
    topology.demo.ngg.io/latency-ms- \
    topology.demo.ngg.io/topology-version- \
    topology.demo.ngg.io/source- \
    topology.demo.ngg.io/local-interface- \
    --overwrite >/dev/null 2>&1 || true
done

kube get nodes -L topology.demo.ngg.io/leaf-switch
