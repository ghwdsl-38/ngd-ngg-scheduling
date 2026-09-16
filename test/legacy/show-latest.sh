#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SUITES_DIR="${ROOT_DIR}/test/legacy"

usage() {
  cat <<'EOF'
用法：
  ./test/legacy/show-latest.sh
  ./test/legacy/show-latest.sh group1_algorithm [cold_cache|warm_cache]
  ./test/legacy/show-latest.sh group2_prc [normal_create]
  ./test/legacy/show-latest.sh group3_full_chain [cold_cache|warm_cache]

每组结果分为 timing-run（性能）和 evidence-run（原始输入、中间过程、输出）。
EOF
}

latest_id() {
  find "$1/runs" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' 2>/dev/null | sort | tail -n 1
}

show_group() {
  local group="$1"
  local selected_case="${2:-}"
  local group_dir="${SUITES_DIR}/${group}"
  [[ -d "${group_dir}" ]] || { printf '未知测试组：%s\n' "${group}" >&2; return 2; }
  local identifier
  identifier="$(latest_id "${group_dir}")"
  [[ -n "${identifier}" ]] || { printf '%s 尚无运行结果\n' "${group}"; return 1; }
  local run_dir="${group_dir}/runs/${identifier}"
  printf '\n===== %s / %s =====\n' "${group}" "${identifier}"
  [[ -f "${run_dir}/report.md" ]] && sed -n '1,160p' "${run_dir}/report.md"

  if [[ -n "${selected_case}" ]]; then
    printf '\n----- Timing -----\n'
    find "${run_dir}/timing-run" -type f -printf '%P\n' 2>/dev/null | sort
    printf '\n----- Evidence / %s -----\n' "${selected_case}"
    find "${run_dir}/evidence-run/${selected_case}" -type f -printf '%P\n' 2>/dev/null | sort
  else
    printf '\n----- 本轮全部文件 -----\n'
    find "${run_dir}" -type f -printf '%P\n' | sort
  fi
  printf '\n完整目录：%s\n' "${run_dir}"
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if [[ $# -eq 0 ]]; then
  show_group group1_algorithm
  show_group group2_prc
  show_group group3_full_chain
else
  show_group "$1" "${2:-}"
fi
