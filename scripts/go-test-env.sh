#!/usr/bin/env bash
# 配置本项目的本地Go Test/Delve环境，工具和缓存默认保存在仓库旁或仓库内。
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
TOOLS_DIR="${TOOLS_DIR:-${ROOT_DIR}/../.tools}"
ENVTEST_VERSION="${ENVTEST_VERSION:-1.35.5}"

if [[ -d "${TOOLS_DIR}/go/bin" ]]; then
  export PATH="${TOOLS_DIR}/go/bin:${PATH}"
fi
if [[ -d "${TOOLS_DIR}/bin" ]]; then
  export PATH="${TOOLS_DIR}/bin:${PATH}"
fi

export GOMODCACHE="${GOMODCACHE:-${ROOT_DIR}/.cache/go-mod}"
export GOCACHE="${GOCACHE:-${ROOT_DIR}/.cache/go-build}"
export KUBEBUILDER_ASSETS="${KUBEBUILDER_ASSETS:-${ROOT_DIR}/.cache/envtest/${ENVTEST_VERSION}}"
export GOMAXPROCS="${GOMAXPROCS:-2}"

mkdir -p "${GOMODCACHE}" "${GOCACHE}"
