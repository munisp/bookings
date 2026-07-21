#!/usr/bin/env bash
# security-scan.sh — OWASP ZAP baseline scan against the OpenDesk gateway
# (SPEC-W3 §2). Runs the official ZAP stable image in Docker against
# http://host.docker.internal:9080 (the APISIX edge port on the host) and
# writes an HTML report to reports/.
#
# Usage:
#   scripts/security-scan.sh [target-url]
#     target-url  defaults to http://host.docker.internal:9080
#
# Notes:
#   - The stack must be up locally (docker compose up) so the gateway answers
#     on the host's :9080. On Linux, host.docker.internal requires docker
#     20.10+ (the script adds --add-host explicitly for older daemons).
#   - Baseline is PASSIVE-ish (non-invasive): it spiders and flags issues but
#     does not run active attacks. Exit code: number of WARN findings would
#     be nonzero with `-I` omitted — we pass -I so WARNs don't fail the run;
#     triage the HTML report instead. CI-adjacent: run on demand/nightly,
#     not as a PR gate until the false-positive list is maintained.
set -euo pipefail

TARGET="${1:-http://host.docker.internal:9080}"
REPORT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/reports"
STAMP="$(date +%Y%m%d-%H%M%S)"
REPORT="zap-baseline-${STAMP}.html"
ZAP_IMAGE="${ZAP_IMAGE:-ghcr.io/zaproxy/zaproxy:stable}"

mkdir -p "${REPORT_DIR}"

echo "[zap] pulling ${ZAP_IMAGE} (skip with ZAP_IMAGE cached)..."
docker pull "${ZAP_IMAGE}" >/dev/null

echo "[zap] baseline scan of ${TARGET} -> reports/${REPORT}"
# -I: ignore WARNs for the exit code; -j: run the baseline job;
#     -r: HTML report name inside the mounted workdir.
docker run --rm -t \
  --add-host host.docker.internal:host-gateway \
  -v "${REPORT_DIR}:/zap/wrk/:rw" \
  "${ZAP_IMAGE}" \
  zap-baseline.py \
  -t "${TARGET}" \
  -r "${REPORT}" \
  -I || true   # report findings without failing the shell on nonzero exits

if [[ -f "${REPORT_DIR}/${REPORT}" ]]; then
  echo "[zap] report written: reports/${REPORT}"
else
  echo "[zap] WARNING: report not produced — is the stack up at ${TARGET}?" >&2
  exit 1
fi
