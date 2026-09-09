#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
RELEASE_VERSION="${RELEASE_VERSION:-v0.6.1}"
DOCKERHUB_REPOSITORY="${DOCKERHUB_REPOSITORY:-ghwdsl/ngd-ngg-scheduling}"
RUN_ID="$(date '+%Y%m%d-%H%M%S.%N')"
RUN_DIR="${SCRIPT_DIR}/results/${RUN_ID}"
PRC_ARCHIVE="${ROOT_DIR}/images/ngd-ngg-prc-${RELEASE_VERSION}-multiarch.oci.tar"
ALGORITHM_ARCHIVE="${ROOT_DIR}/images/ngd-ngg-algorithm-${RELEASE_VERSION}-multiarch.oci.tar"
PRC_IMAGE="${DOCKERHUB_REPOSITORY}:prc-${RELEASE_VERSION}-local-amd64"
ALGORITHM_IMAGE="${DOCKERHUB_REPOSITORY}:algorithm-${RELEASE_VERSION}-local-amd64"

mkdir -p "${RUN_DIR}"
test -f "${PRC_ARCHIVE}" || { echo "ERROR: missing ${PRC_ARCHIVE}" >&2; exit 1; }
test -f "${ALGORITHM_ARCHIVE}" || { echo "ERROR: missing ${ALGORITHM_ARCHIVE}" >&2; exit 1; }

python3 "${ROOT_DIR}/scripts/inspect-oci-platforms.py" \
  "${PRC_ARCHIVE}" "${ALGORITHM_ARCHIVE}" | tee "${RUN_DIR}/oci-platforms.txt"

docker image inspect "${PRC_IMAGE}" "${ALGORITHM_IMAGE}" \
  --format '{{json .}}' >"${RUN_DIR}/local-amd64-images.jsonl"

source "${ROOT_DIR}/scripts/go-test-env.sh"
export RELEASE_VERSION DOCKERHUB_REPOSITORY
export PRC_RELEASE_TEST_IMAGE="${PRC_IMAGE}"
export ALGORITHM_RELEASE_TEST_IMAGE="${ALGORITHM_IMAGE}"
export IMAGE_VALIDATION_RUN_DIR="${RUN_DIR}"

cd "${ROOT_DIR}"
go test -p=1 ./go_test_suites/image_release \
  -run '^TestReleaseImagesWithEnvtest$' -v -count=1 -timeout=5m 2>&1 |
  tee "${RUN_DIR}/go-test.log"

echo "PASS: release image validation results=${RUN_DIR}"
