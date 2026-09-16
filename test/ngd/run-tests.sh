#!/usr/bin/env bash
set -u

KUBECTL=/usr/local/bin/kubectl
BASE_DIR=/root/ghw/test/ngd
RESULT_DIR="${BASE_DIR}/results"

mkdir -p "${RESULT_DIR}/raw"
printf 'case\tphase\tresolvedNodes\tmessage\tnggPhase\tnodes\tcpu\tmemory\n' > "${RESULT_DIR}/summary.tsv"

# 清理以前的演示对象与本测试集可能残留的对象。
${KUBECTL} delete ngd go-test-demand --ignore-not-found --wait=true >/dev/null 2>&1 || true
for old in $(${KUBECTL} get ngd -l ngg.demo/test-suite=three-node -o name 2>/dev/null); do
  ${KUBECTL} delete "${old}" --wait=true >/dev/null 2>&1 || true
done

for file in "${BASE_DIR}"/[0-9][0-9]-*.yaml; do
  name=$(${KUBECTL} create --dry-run=client -f "${file}" -o jsonpath='{.metadata.name}')
  echo "===== ${name} ====="
  ${KUBECTL} apply -f "${file}"

  phase=""
  for _ in $(seq 1 25); do
    phase=$(${KUBECTL} get ngd "${name}" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    if [ "${phase}" = "Fulfilled" ] || [ "${phase}" = "Failed" ]; then
      break
    fi
    sleep 2
  done

  ${KUBECTL} get ngd "${name}" -o yaml > "${RESULT_DIR}/raw/${name}-ngd.yaml"
  count=$(${KUBECTL} get ngd "${name}" -o jsonpath='{.status.resolvedNodeCount}' 2>/dev/null || true)
  message=$(${KUBECTL} get ngd "${name}" -o jsonpath='{.status.message}' 2>/dev/null || true)
  ngg="ngg-${name}"
  ngg_phase=""
  nodes=""
  cpu=""
  memory=""
  if ${KUBECTL} get ngg "${ngg}" >/dev/null 2>&1; then
    ${KUBECTL} get ngg "${ngg}" -o yaml > "${RESULT_DIR}/raw/${name}-ngg.yaml"
    ngg_phase=$(${KUBECTL} get ngg "${ngg}" -o jsonpath='{.status.phase}')
    nodes=$(${KUBECTL} get ngg "${ngg}" -o jsonpath='{range .spec.nodes[*]}{.name}{","}{end}')
    cpu=$(${KUBECTL} get ngg "${ngg}" -o jsonpath='{.status.resolvedCapacity.cpu}')
    memory=$(${KUBECTL} get ngg "${ngg}" -o jsonpath='{.status.resolvedCapacity.memory}')
  fi
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "${name}" "${phase:-TimedOut}" "${count}" "${message}" "${ngg_phase}" \
    "${nodes}" "${cpu}" "${memory}" >> "${RESULT_DIR}/summary.tsv"
  echo "phase=${phase:-TimedOut} nodes=${nodes:-none} message=${message}"

  ${KUBECTL} delete ngd "${name}" --wait=true >/dev/null
  sleep 2
done

${KUBECTL} get ngd,ngg -A > "${RESULT_DIR}/remaining-resources.txt"
cat "${RESULT_DIR}/summary.tsv"
