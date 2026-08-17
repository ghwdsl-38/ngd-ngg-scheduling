#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

VOLCANO_COMMIT="8fc394c11e8db0d0ada5c17816b58bced9d7213d"
VOLCANO_SOURCE="${ROOT_DIR}/../third_party/volcano"
BUILD_SOURCE="${ROOT_DIR}/.cache/volcano-v1.15.0"
GO_MODULE_PROXY="${GO_MODULE_PROXY:-https://goproxy.cn,direct}"
GO_MOD_CACHE="${ROOT_DIR}/.cache/go-mod"
GO_BUILD_CACHE="${ROOT_DIR}/.cache/go-build"
PATCH_FILE="${ROOT_DIR}/patches/volcano-v1.15.0-nodegroupgrant-register.patch"

require_cmd docker
require_cmd git
docker info >/dev/null || die "Docker daemon不可用"
[[ -d "${VOLCANO_SOURCE}/.git" ]] || die "缺少Volcano源码 ${VOLCANO_SOURCE}"
[[ "$(git -C "${VOLCANO_SOURCE}" rev-parse HEAD)" == "${VOLCANO_COMMIT}" ]] ||
  die "Volcano源码提交不是锁定的v1.15.0提交"

mkdir -p "${ROOT_DIR}/.cache" "${GO_MOD_CACHE}" "${GO_BUILD_CACHE}"
if [[ ! -e "${BUILD_SOURCE}/.git" ]]; then
  git -C "${VOLCANO_SOURCE}" worktree add --detach "${BUILD_SOURCE}" "${VOLCANO_COMMIT}"
fi
[[ "$(git -C "${BUILD_SOURCE}" rev-parse HEAD)" == "${VOLCANO_COMMIT}" ]] ||
  die "构建Worktree提交不匹配"

mkdir -p "${BUILD_SOURCE}/pkg/scheduler/plugins/nodegroupgrant"
cp "${ROOT_DIR}/plugin/nodegroupgrant/"*.go \
  "${BUILD_SOURCE}/pkg/scheduler/plugins/nodegroupgrant/"
if ! grep -q 'plugins/nodegroupgrant' "${BUILD_SOURCE}/pkg/scheduler/plugins/factory.go"; then
  git -C "${BUILD_SOURCE}" apply --check "${PATCH_FILE}"
  git -C "${BUILD_SOURCE}" apply "${PATCH_FILE}"
fi

log "在/mnt/data0缓存Go依赖并测试nodegroupgrant插件"
docker run --rm \
  -e "GOPROXY=${GO_MODULE_PROXY}" \
  -e GOMODCACHE=/cache/mod \
  -e GOCACHE=/cache/build \
  -v "${BUILD_SOURCE}:/workspace" \
  -v "${VOLCANO_SOURCE}/.git:${VOLCANO_SOURCE}/.git:ro" \
  -v "${GO_MOD_CACHE}:/cache/mod" \
  -v "${GO_BUILD_CACHE}:/cache/build" \
  -w /workspace \
  golang:1.25.0 \
  sh -c 'gofmt -w pkg/scheduler/plugins/nodegroupgrant/*.go && go test ./pkg/scheduler/plugins/nodegroupgrant' \
  2>&1 | tee "${RESULTS_DIR}/plugin-test.log"

if [[ "${PLUGIN_TEST_ONLY:-0}" == "1" ]]; then
  log "PLUGIN_TEST_ONLY=1，仅完成插件单元测试"
  exit 0
fi

log "编译带NGG Filter的vc-scheduler"
docker run --rm \
  -e "GOPROXY=${GO_MODULE_PROXY}" \
  -e GOMODCACHE=/cache/mod \
  -e GOCACHE=/cache/build \
  -v "${BUILD_SOURCE}:/workspace" \
  -v "${VOLCANO_SOURCE}/.git:${VOLCANO_SOURCE}/.git:ro" \
  -v "${GO_MOD_CACHE}:/cache/mod" \
  -v "${GO_BUILD_CACHE}:/cache/build" \
  -w /workspace \
  golang:1.25.0 \
  make vc-scheduler \
  2>&1 | tee "${RESULTS_DIR}/scheduler-build.log"

binary="${BUILD_SOURCE}/_output/bin/vc-scheduler"
[[ -x "${binary}" ]] || die "未找到编译产物 ${binary}"
docker buildx build --load \
  --tag "${SCHEDULER_IMAGE}" \
  --file "${ROOT_DIR}/plugin/Dockerfile.runtime" \
  "$(dirname "${binary}")" \
  2>&1 | tee "${RESULTS_DIR}/scheduler-image-build.log"
docker image inspect "${SCHEDULER_IMAGE}" --format '{{.Id}} {{.RepoTags}}' |
  tee "${RESULTS_DIR}/scheduler-image.txt"
docker save "${SCHEDULER_IMAGE}" -o "${IMAGES_DIR}/volcano-ngg-scheduler-v1.15.0.tar"
chmod 0644 "${IMAGES_DIR}/volcano-ngg-scheduler-v1.15.0.tar"
if kind get clusters 2>/dev/null | grep -Fxq "${CLUSTER_NAME}"; then
  kind load docker-image "${SCHEDULER_IMAGE}" --name "${CLUSTER_NAME}"
fi
