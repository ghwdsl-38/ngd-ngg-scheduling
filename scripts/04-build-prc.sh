#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

require_cmd docker
docker info >/dev/null || die "Docker daemon不可用"
mkdir -p "${ROOT_DIR}/build" "${ROOT_DIR}/.cache/go-mod" "${ROOT_DIR}/.cache/go-build"
docker run --rm \
  -e GOMODCACHE=/cache/mod \
  -e GOCACHE=/cache/build \
  -e CGO_ENABLED=0 \
  -v "${ROOT_DIR}/prc:/workspace" \
  -v "${ROOT_DIR}/build:/out" \
  -v "${ROOT_DIR}/.cache/go-mod:/cache/mod" \
  -v "${ROOT_DIR}/.cache/go-build:/cache/build" \
  -w /workspace \
  golang:1.25.0 \
  sh -c 'go build -trimpath -ldflags="-s -w" -o /out/prc ./cmd'
docker build --tag "${PRC_IMAGE}" --file "${ROOT_DIR}/Dockerfile.prc" "${ROOT_DIR}"
docker image inspect "${PRC_IMAGE}" --format '{{.Id}} {{.RepoTags}}' |
  tee "${RESULTS_DIR}/prc-image.txt"
docker save "${PRC_IMAGE}" -o "${IMAGES_DIR}/ngd-ngg-prc-v0.3.0.tar"
chmod 0644 "${IMAGES_DIR}/ngd-ngg-prc-v0.3.0.tar"
