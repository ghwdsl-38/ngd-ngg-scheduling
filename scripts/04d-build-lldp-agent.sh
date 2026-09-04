#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

require_cmd docker
docker build --tag "${LLDP_AGENT_IMAGE}" \
  --file "${ROOT_DIR}/Dockerfile.lldp-agent" "${ROOT_DIR}"
docker image inspect "${LLDP_AGENT_IMAGE}" --format '{{.Id}} {{.RepoTags}}' |
  tee "${RESULTS_DIR}/lldp-agent-image.txt"
docker save "${LLDP_AGENT_IMAGE}" \
  -o "${IMAGES_DIR}/ngd-ngg-lldp-agent-v0.2.0.tar"
chmod 0644 "${IMAGES_DIR}/ngd-ngg-lldp-agent-v0.2.0.tar"
