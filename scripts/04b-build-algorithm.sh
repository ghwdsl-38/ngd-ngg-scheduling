#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

require_cmd docker
docker info >/dev/null || die "Docker daemon不可用"
docker build --tag "${ALGORITHM_IMAGE}" --file "${ROOT_DIR}/Dockerfile.algorithm" "${ROOT_DIR}"
docker image inspect "${ALGORITHM_IMAGE}" --format '{{.Id}} {{.RepoTags}}' |
  tee "${RESULTS_DIR}/algorithm-image.txt"
docker save "${ALGORITHM_IMAGE}" -o "${IMAGES_DIR}/ngd-ngg-algorithm-v0.3.0.tar"
chmod 0644 "${IMAGES_DIR}/ngd-ngg-algorithm-v0.3.0.tar"
if kind get clusters 2>/dev/null | grep -Fxq "${CLUSTER_NAME}"; then
  kind load docker-image "${ALGORITHM_IMAGE}" --name "${CLUSTER_NAME}"
fi
