#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${ROOT_DIR}"
PYTHONPATH=algorithm_server/python:src python3 -m unittest discover -s tests -p 'test_project_algorithm_contract.py' -v

if command -v go >/dev/null 2>&1; then
  (cd "${ROOT_DIR}/prc" && go test ./...)
  exit 0
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: 既没有 go，也没有 docker，无法运行 PRC Go 衔接测试" >&2
  exit 1
fi

mkdir -p "${ROOT_DIR}/.cache/go/pkg/mod" "${ROOT_DIR}/.cache/go/build"
docker run --rm -e GOPROXY=https://goproxy.cn,direct -e GOCACHE=/cache/go-build -e GOMODCACHE=/go/pkg/mod -v "${ROOT_DIR}/prc:/workspace/prc:ro" -v "${ROOT_DIR}/.cache/go/pkg/mod:/go/pkg/mod" -v "${ROOT_DIR}/.cache/go/build:/cache/go-build" -w /workspace/prc golang:1.25-alpine go test ./...
