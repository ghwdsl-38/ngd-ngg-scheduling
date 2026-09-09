#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

RELEASE_VERSION="${RELEASE_VERSION:-v0.6.1}"
GO_BUILDER_IMAGE="${GO_BUILDER_IMAGE:-golang:1.25.0}"

[[ "${RELEASE_VERSION}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] ||
  die "RELEASE_VERSION包含非法字符: ${RELEASE_VERSION}"
require_cmd docker
docker info >/dev/null || die "Docker daemon不可用"

VCS_REF="$(git -C "${ROOT_DIR}" rev-parse --short=12 HEAD 2>/dev/null || printf unknown)"
BUILD_ROOT="${ROOT_DIR}/build/release/${RELEASE_VERSION}"
mkdir -p "${BUILD_ROOT}" "${ROOT_DIR}/.cache/go-mod" "${ROOT_DIR}/.cache/go-build"

build_component() {
  local component="$1"
  local module_dir="$2"
  local package_path="$3"
  local output_name="$4"
  local arch="$5"
  local module_mode="$6"
  local output_dir="${BUILD_ROOT}/linux-${arch}"

  mkdir -p "${output_dir}"
  log "Go预编译开始 component=${component} os=linux arch=${arch} version=${RELEASE_VERSION}"
  docker run --rm \
    -e CGO_ENABLED=0 \
    -e GOOS=linux \
    -e GOARCH="${arch}" \
    -e GOWORK=off \
    -e GOMAXPROCS=2 \
    -e GOMODCACHE=/cache/mod \
    -e GOCACHE=/cache/build \
    -v "${ROOT_DIR}:/workspace" \
    -v "${ROOT_DIR}/.cache/go-mod:/cache/mod" \
    -v "${ROOT_DIR}/.cache/go-build:/cache/build" \
    -w "/workspace/${module_dir}" \
    "${GO_BUILDER_IMAGE}" \
    go build "${module_mode}" -p=1 -trimpath -ldflags="-s -w" \
      -o "/workspace/build/release/${RELEASE_VERSION}/linux-${arch}/${output_name}" \
      "${package_path}"
  test -x "${output_dir}/${output_name}" || die "未生成二进制 ${output_dir}/${output_name}"
  log "Go预编译完成 component=${component} file=${output_dir}/${output_name}"
}

for target_arch in amd64 arm64; do
  build_component prc prc ./cmd prc "${target_arch}" -mod=mod
  build_component algorithm algorithm_server/go ./cmd/algorithm-server algorithm-server "${target_arch}" -mod=vendor
done

sha256sum \
  "${BUILD_ROOT}/linux-amd64/prc" \
  "${BUILD_ROOT}/linux-amd64/algorithm-server" \
  "${BUILD_ROOT}/linux-arm64/prc" \
  "${BUILD_ROOT}/linux-arm64/algorithm-server" >"${BUILD_ROOT}/SHA256SUMS"

{
  printf 'version=%s\n' "${RELEASE_VERSION}"
  printf 'vcsRef=%s\n' "${VCS_REF}"
  printf 'goBuilderImage=%s\n' "${GO_BUILDER_IMAGE}"
  printf 'platforms=linux/amd64,linux/arm64\n'
} >"${BUILD_ROOT}/BUILD-INFO.txt"

log "双架构Go二进制准备完成: ${BUILD_ROOT}"
