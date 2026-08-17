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
    switch_id="switch-a"; bandwidth="20"; latency="1.5"
  elif (( ordinal <= 5 )); then
    switch_id="switch-b"; bandwidth="10"; latency="5"
  else
    switch_id="switch-c"; bandwidth="25"; latency="1"
  fi
  node_name="${workers[index]}"
  kube label node "${node_name}" \
    demo.ngg/worker=true \
    topology.demo.ngg.io/switch="${switch_id}" \
    topology.demo.ngg.io/core-switch=core-0 \
    topology.demo.ngg.io/bandwidth-gbps="${bandwidth}" \
    topology.demo.ngg.io/latency-ms="${latency}" \
    topology.demo.ngg.io/local-interface=eth0 \
    --overwrite
  kube annotate node "${node_name}" \
    topology.demo.ngg.io/remote-port="Ethernet1/${ordinal}" --overwrite
done

kube get nodes -L topology.demo.ngg.io/switch
