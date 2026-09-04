#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

for command_name in docker kubectl git python3; do
  require_cmd "${command_name}"
done
docker info >/dev/null || die "Docker daemon不可用"
docker_root="$(docker info --format '{{.DockerRootDir}}')"
docker_available_kb="$(df -Pk "${docker_root}" | awk 'NR==2 {print $4}')"
if [[ "${docker_root}" != /mnt/data0/* ]] && (( docker_available_kb < 4 * 1024 * 1024 )); then
  die "Docker数据目录 ${docker_root} 只有$((docker_available_kb / 1024 / 1024))GiB可用；建议迁移到/mnt/data0"
fi
df -h / /mnt/data0
python3 --version
kubectl version --client
log "环境检查通过；工程和构建缓存均位于 ${ROOT_DIR}"
log "注意：DockerRootDir必须有足够空间；镜像tar归档将写入 ${IMAGES_DIR}"
