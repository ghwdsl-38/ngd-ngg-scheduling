#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

RELEASE_VERSION="${RELEASE_VERSION:-v0.6.1}"
DOCKERHUB_REPOSITORY="${DOCKERHUB_REPOSITORY:-ghwdsl/ngd-ngg-scheduling}"
BUILDX_BUILDER="${BUILDX_BUILDER:-ngd-ngg-release}"
BUILD_ROOT="${ROOT_DIR}/build/release/${RELEASE_VERSION}"
VCS_REF="$(git -C "${ROOT_DIR}" rev-parse --short=12 HEAD 2>/dev/null || printf unknown)"

require_cmd docker
docker info >/dev/null || die "Docker daemon不可用"
for artifact in \
  linux-amd64/prc linux-amd64/algorithm-server \
  linux-arm64/prc linux-arm64/algorithm-server; do
  test -x "${BUILD_ROOT}/${artifact}" || die "缺少预编译文件 ${BUILD_ROOT}/${artifact}"
done
docker buildx inspect "${BUILDX_BUILDER}" >/dev/null 2>&1 ||
  die "未找到builder ${BUILDX_BUILDER}，请先运行04-release-package-images.sh"

for component in prc algorithm; do
  dockerfile="${ROOT_DIR}/Dockerfile.${component}.release"
  image="${DOCKERHUB_REPOSITORY}:${component}-${RELEASE_VERSION}"
  log "Push多架构镜像: ${image}"
  docker buildx build --builder "${BUILDX_BUILDER}" \
    --platform linux/amd64,linux/arm64 \
    --provenance=false --sbom=false \
    --build-arg RELEASE_VERSION="${RELEASE_VERSION}" \
    --build-arg VCS_REF="${VCS_REF}" \
    --tag "${image}" --push \
    --file "${dockerfile}" "${ROOT_DIR}"
  docker buildx imagetools inspect "${image}"
done

log "Docker Hub发布完成"
