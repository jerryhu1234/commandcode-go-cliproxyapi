#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CPA_SOURCE="${CPA_SOURCE:?set CPA_SOURCE to a CLIProxyAPI v7.3.15 source checkout}"
GO_BIN="${GO_BIN:-go}"
CPA_HOST_WORK="${CPA_HOST_WORK:-${TMPDIR:-/tmp}}"
GO_WORK_ROOT="${GO_WORK_ROOT:-${TMPDIR:-/tmp}}"
PLUGIN_ID=commandcode-go-cliproxyapi

if [[ ! -d "${CPA_SOURCE}" || ! -f "${CPA_SOURCE}/go.mod" ]]; then
  echo "CPA_SOURCE must be a CLIProxyAPI source checkout" >&2
  exit 2
fi
mkdir -p "${CPA_HOST_WORK}" "${GO_WORK_ROOT}"

owned_paths=()
cleanup() {
  local path
  for path in "${owned_paths[@]}"; do
    if [[ -n "${path}" && -d "${path}" ]]; then
      rm -rf -- "${path}"
    fi
  done
}
trap cleanup EXIT

WORK="$(mktemp -d "${CPA_HOST_WORK%/}/commandcode-cpa-host.XXXXXX")"
owned_paths+=("${WORK}")
GO_WORK="$(mktemp -d "${GO_WORK_ROOT%/}/commandcode-go-work.XXXXXX")"
owned_paths+=("${GO_WORK}")
HARNESS_DIR="$(mktemp -d "${CPA_SOURCE%/}/commandcode-host-integration.XXXXXX")"
owned_paths+=("${HARNESS_DIR}")

mkdir -p "${WORK}/plugins" "${WORK}/auth" "${GO_WORK}/gopath" "${GO_WORK}/cpa-gopath" "${GO_WORK}/cache" "${GO_WORK}/tmp"

env -u GOROOT PATH="$(dirname "${GO_BIN}"):${PATH}" \
  GOPATH="${GO_WORK}/gopath" GOCACHE="${GO_WORK}/cache" \
  GOTMPDIR="${GO_WORK}/tmp" TMPDIR="${GO_WORK}/tmp" GOENV=off \
  CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  "${GO_BIN}" build -trimpath -buildmode=c-shared \
    -ldflags "-s -w -X commandcode-go-cliproxyapi/internal/buildinfo.Version=0.2.0-dev.5 -X commandcode-go-cliproxyapi/internal/buildinfo.Commit=cpa-host-integration" \
    -o "${WORK}/plugins/${PLUGIN_ID}.so" "${ROOT}"

HARNESS="${HARNESS_DIR}/main.go"
cp "${ROOT}/tests/cpa_host_integration_harness.go.txt" "${HARNESS}"

(
  cd "${CPA_SOURCE}"
  env -u GOROOT PATH="$(dirname "${GO_BIN}"):${PATH}" \
    GOPATH="${CPA_GOPATH:-${GO_WORK}/cpa-gopath}" GOCACHE="${GO_WORK}/cache" \
    GOTMPDIR="${GO_WORK}/tmp" TMPDIR="${GO_WORK}/tmp" GOENV=off \
    NO_PROXY="${NO_PROXY:-localhost,127.0.0.1,::1}" no_proxy="${no_proxy:-localhost,127.0.0.1,::1}" \
    "${GO_BIN}" run "./$(basename "${HARNESS_DIR}")/main.go" "${WORK}/plugins" "${WORK}/auth"
)
