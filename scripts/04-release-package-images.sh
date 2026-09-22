#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

RELEASE_VERSION="${RELEASE_VERSION:-v0.6.2}"
DOCKERHUB_REPOSITORY="${DOCKERHUB_REPOSITORY:-ghwdsl/ngd-ngg-scheduling}"
BUILDX_BUILDER="${BUILDX_BUILDER:-ngd-ngg-release}"
RELEASE_COMPONENTS="${RELEASE_COMPONENTS:-prc algorithm lldp}"
BUILD_ROOT="${ROOT_DIR}/build/release/${RELEASE_VERSION}"
VCS_REF="$(git -C "${ROOT_DIR}" rev-parse --short=12 HEAD 2>/dev/null || printf unknown)"

components=()
for component in ${RELEASE_COMPONENTS}; do
  case "${component}" in
    prc | algorithm | lldp) components+=("${component}") ;;
    *) die "未知发布组件: ${component}（允许: prc algorithm lldp）" ;;
  esac
done
((${#components[@]} > 0)) || die "RELEASE_COMPONENTS不能为空"

component_metadata() {
  case "$1" in
    prc)
      binary="prc"
      local_image="${DOCKERHUB_REPOSITORY}:prc-${RELEASE_VERSION}-local-amd64"
      archive="${IMAGES_DIR}/ngd-ngg-prc-${RELEASE_VERSION}-multiarch.oci.tar"
      ;;
    algorithm)
      binary="algorithm-server"
      local_image="${DOCKERHUB_REPOSITORY}:algorithm-${RELEASE_VERSION}-local-amd64"
      archive="${IMAGES_DIR}/ngd-ngg-algorithm-${RELEASE_VERSION}-multiarch.oci.tar"
      ;;
    lldp)
      binary="topology-agent"
      local_image="${DOCKERHUB_REPOSITORY}:lldp-${RELEASE_VERSION}-local-amd64"
      archive="${IMAGES_DIR}/ngd-ngg-lldp-${RELEASE_VERSION}-multiarch.oci.tar"
      ;;
  esac
  dockerfile="${ROOT_DIR}/docker/$1/Dockerfile.release"
}

require_cmd docker
docker info >/dev/null || die "Docker daemon不可用"

local_images=()
archives=()
for component in "${components[@]}"; do
  component_metadata "${component}"
  for arch in amd64 arm64; do
    test -x "${BUILD_ROOT}/linux-${arch}/${binary}" ||
      die "缺少预编译文件 ${BUILD_ROOT}/linux-${arch}/${binary}，请先构建${component}二进制"
  done

  log "构建本机amd64测试镜像 component=${component} image=${local_image}"
  docker build --platform linux/amd64 \
    --build-arg TARGETARCH=amd64 \
    --build-arg RELEASE_VERSION="${RELEASE_VERSION}" \
    --build-arg VCS_REF="${VCS_REF}" \
    --tag "${local_image}" \
    --file "${dockerfile}" "${ROOT_DIR}"
  local_images+=("${local_image}")
  archives+=("${archive}")
done

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

for component in "${components[@]}"; do
  component_metadata "${component}"
  rm -f "${archive}"
  log "生成amd64+arm64 OCI镜像包 component=${component} archive=${archive}"
  docker buildx build --builder "${BUILDX_BUILDER}" \
    --platform linux/amd64,linux/arm64 \
    --provenance=false --sbom=false \
    --build-arg RELEASE_VERSION="${RELEASE_VERSION}" \
    --build-arg VCS_REF="${VCS_REF}" \
    --output "type=oci,dest=${archive}" \
    --file "${dockerfile}" "${ROOT_DIR}"
done

sha256sum "${archives[@]}" >"${IMAGES_DIR}/SHA256SUMS"
docker image inspect "${local_images[@]}" \
  --format '{{json .}}' >"${IMAGES_DIR}/local-amd64-image-inspect.jsonl"

log "镜像打包完成；未执行Docker Hub Push components=${components[*]}"
for component in "${components[@]}"; do
  component_metadata "${component}"
  log "${component}本地测试镜像=${local_image}"
done
