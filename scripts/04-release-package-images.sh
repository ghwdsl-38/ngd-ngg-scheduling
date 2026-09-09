#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

RELEASE_VERSION="${RELEASE_VERSION:-v0.6.1}"
DOCKERHUB_REPOSITORY="${DOCKERHUB_REPOSITORY:-ghwdsl/ngd-ngg-scheduling}"
BUILDX_BUILDER="${BUILDX_BUILDER:-ngd-ngg-release}"
BUILD_ROOT="${ROOT_DIR}/build/release/${RELEASE_VERSION}"
VCS_REF="$(git -C "${ROOT_DIR}" rev-parse --short=12 HEAD 2>/dev/null || printf unknown)"
PRC_LOCAL_IMAGE="${DOCKERHUB_REPOSITORY}:prc-${RELEASE_VERSION}-local-amd64"
ALGORITHM_LOCAL_IMAGE="${DOCKERHUB_REPOSITORY}:algorithm-${RELEASE_VERSION}-local-amd64"
PRC_ARCHIVE="${IMAGES_DIR}/ngd-ngg-prc-${RELEASE_VERSION}-multiarch.oci.tar"
ALGORITHM_ARCHIVE="${IMAGES_DIR}/ngd-ngg-algorithm-${RELEASE_VERSION}-multiarch.oci.tar"

require_cmd docker
docker info >/dev/null || die "Docker daemon不可用"
for artifact in \
  linux-amd64/prc linux-amd64/algorithm-server \
  linux-arm64/prc linux-arm64/algorithm-server; do
  test -x "${BUILD_ROOT}/${artifact}" ||
    die "缺少预编译文件 ${BUILD_ROOT}/${artifact}，请先运行04-release-build-binaries.sh"
done

log "构建本机amd64 PRC测试镜像: ${PRC_LOCAL_IMAGE}"
docker build --platform linux/amd64 \
  --build-arg TARGETARCH=amd64 \
  --build-arg RELEASE_VERSION="${RELEASE_VERSION}" \
  --build-arg VCS_REF="${VCS_REF}" \
  --tag "${PRC_LOCAL_IMAGE}" \
  --file "${ROOT_DIR}/Dockerfile.prc.release" "${ROOT_DIR}"

log "构建本机amd64 Algorithm测试镜像: ${ALGORITHM_LOCAL_IMAGE}"
docker build --platform linux/amd64 \
  --build-arg TARGETARCH=amd64 \
  --build-arg RELEASE_VERSION="${RELEASE_VERSION}" \
  --build-arg VCS_REF="${VCS_REF}" \
  --tag "${ALGORITHM_LOCAL_IMAGE}" \
  --file "${ROOT_DIR}/Dockerfile.algorithm.release" "${ROOT_DIR}"

if ! docker buildx inspect "${BUILDX_BUILDER}" >/dev/null 2>&1; then
  log "创建隔离的Buildx builder: ${BUILDX_BUILDER}"
  builder_options=(--name "${BUILDX_BUILDER}" --driver docker-container --driver-opt network=host)
  if [[ -n "${HTTP_PROXY:-}" ]]; then
    builder_options+=(--driver-opt "env.HTTP_PROXY=${HTTP_PROXY}")
  fi
  if [[ -n "${HTTPS_PROXY:-}" ]]; then
    builder_options+=(--driver-opt "env.HTTPS_PROXY=${HTTPS_PROXY}")
  fi
  if [[ -n "${NO_PROXY:-}" ]]; then
    builder_options+=(--driver-opt "env.NO_PROXY=${NO_PROXY}")
  fi
  docker buildx create "${builder_options[@]}" >/dev/null
fi
docker buildx inspect "${BUILDX_BUILDER}" --bootstrap >/dev/null

rm -f "${PRC_ARCHIVE}" "${ALGORITHM_ARCHIVE}"
log "生成PRC amd64+arm64 OCI镜像包: ${PRC_ARCHIVE}"
docker buildx build --builder "${BUILDX_BUILDER}" \
  --platform linux/amd64,linux/arm64 \
  --provenance=false --sbom=false \
  --build-arg RELEASE_VERSION="${RELEASE_VERSION}" \
  --build-arg VCS_REF="${VCS_REF}" \
  --output "type=oci,dest=${PRC_ARCHIVE}" \
  --file "${ROOT_DIR}/Dockerfile.prc.release" "${ROOT_DIR}"

log "生成Algorithm amd64+arm64 OCI镜像包: ${ALGORITHM_ARCHIVE}"
docker buildx build --builder "${BUILDX_BUILDER}" \
  --platform linux/amd64,linux/arm64 \
  --provenance=false --sbom=false \
  --build-arg RELEASE_VERSION="${RELEASE_VERSION}" \
  --build-arg VCS_REF="${VCS_REF}" \
  --output "type=oci,dest=${ALGORITHM_ARCHIVE}" \
  --file "${ROOT_DIR}/Dockerfile.algorithm.release" "${ROOT_DIR}"

sha256sum "${PRC_ARCHIVE}" "${ALGORITHM_ARCHIVE}" >"${IMAGES_DIR}/SHA256SUMS"
docker image inspect "${PRC_LOCAL_IMAGE}" "${ALGORITHM_LOCAL_IMAGE}" \
  --format '{{json .}}' >"${IMAGES_DIR}/local-amd64-image-inspect.jsonl"

log "镜像打包完成；未执行Docker Hub Push"
log "PRC本地测试镜像=${PRC_LOCAL_IMAGE}"
log "Algorithm本地测试镜像=${ALGORITHM_LOCAL_IMAGE}"
