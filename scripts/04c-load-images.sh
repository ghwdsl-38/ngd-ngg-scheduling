#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

require_cmd docker
archives=(
  "${IMAGES_DIR}/ngd-ngg-prc-v0.3.0.tar"
  "${IMAGES_DIR}/ngd-ngg-algorithm-v0.4.0.tar"
  "${IMAGES_DIR}/ngd-ngg-lldp-agent-v0.2.0.tar"
  "${IMAGES_DIR}/volcano-ngg-scheduler-v1.15.0.tar"
  "${IMAGES_DIR}/ngg-kube-scheduler-v1.35.3.tar"
)
for archive in "${archives[@]}"; do
  [[ -f "${archive}" ]] || die "缺少镜像归档 ${archive}"
  docker load -i "${archive}"
done
log "已从 ${IMAGES_DIR} 恢复全部Demo镜像"
