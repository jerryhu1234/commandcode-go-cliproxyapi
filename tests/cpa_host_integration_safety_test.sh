#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/commandcode-script-safety.XXXXXX")"
trap 'rm -rf -- "${TEST_ROOT}"' EXIT

CPA_SOURCE="${TEST_ROOT}/cpa"
WORK_PARENT="${TEST_ROOT}/work-parent"
GO_PARENT="${TEST_ROOT}/go-parent"
BIN_DIR="${TEST_ROOT}/bin"
mkdir -p "${CPA_SOURCE}" "${WORK_PARENT}" "${GO_PARENT}" "${BIN_DIR}"
: > "${CPA_SOURCE}/go.mod"
: > "${CPA_SOURCE}/cpa_commandcode_host_integration_tmp.go"
: > "${WORK_PARENT}/sentinel"
: > "${GO_PARENT}/sentinel"

cat > "${BIN_DIR}/fake-go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "build" ]]; then
  while (($#)); do
    if [[ "$1" == "-o" ]]; then
      shift
      mkdir -p "$(dirname "$1")"
      : > "$1"
      exit 0
    fi
    shift
  done
  exit 3
fi
if [[ "${1:-}" == "run" ]]; then
  exit "${FAKE_GO_RUN_EXIT:-0}"
fi
exit 4
EOF
chmod +x "${BIN_DIR}/fake-go"

assert_preserved_and_clean() {
  [[ -f "${WORK_PARENT}/sentinel" ]]
  [[ -f "${GO_PARENT}/sentinel" ]]
  [[ -f "${CPA_SOURCE}/cpa_commandcode_host_integration_tmp.go" ]]
  [[ -z "$(find "${WORK_PARENT}" -mindepth 1 ! -name sentinel -print -quit)" ]]
  [[ -z "$(find "${GO_PARENT}" -mindepth 1 ! -name sentinel -print -quit)" ]]
  [[ -z "$(find "${CPA_SOURCE}" -mindepth 1 ! -name go.mod ! -name cpa_commandcode_host_integration_tmp.go -print -quit)" ]]
}

CPA_SOURCE="${CPA_SOURCE}" GO_BIN="${BIN_DIR}/fake-go" \
  CPA_HOST_WORK="${WORK_PARENT}" GO_WORK_ROOT="${GO_PARENT}" \
  bash "${ROOT}/tests/cpa_host_integration.sh"
assert_preserved_and_clean

if CPA_SOURCE="${CPA_SOURCE}" GO_BIN="${BIN_DIR}/fake-go" \
  CPA_HOST_WORK="${WORK_PARENT}" GO_WORK_ROOT="${GO_PARENT}" FAKE_GO_RUN_EXIT=17 \
  bash "${ROOT}/tests/cpa_host_integration.sh"; then
  echo "expected simulated harness failure" >&2
  exit 1
fi
assert_preserved_and_clean

echo "CPA_HOST_SCRIPT_SAFETY_OK sentinel=preserved legacy_harness=preserved owned_cleanup=ok"
