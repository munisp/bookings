#!/bin/bash
# install-pack.sh — install a community industry pack (STRATEGY §3, Wave 5 #6).
#
#   ./scripts/install-pack.sh <url-or-path> [--version V] [--author A]
#
# Downloads (or copies) the pack YAML, validates it against the SPEC-CRM §C
# schema (scripts/validate_pack.py — the same rules identity-service enforces
# at load time), installs it as industries/<id>.yaml, and records id /
# version / sha256 / author / signature in the pack registry
# (industries/index.json). Signature verification is a no-op until the
# sigstore design in docs/marketplace.md lands — unsigned packs install with
# signature: null and a warning.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INDUSTRIES="${ROOT}/industries"
INDEX="${INDUSTRIES}/index.json"
VERSION="0.1.0"
AUTHOR="community"

SRC=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --author)  AUTHOR="$2"; shift 2 ;;
    -h|--help) sed -n '2,14p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *)         SRC="$1"; shift ;;
  esac
done

if [[ -z "${SRC}" ]]; then
  echo "usage: install-pack.sh <url-or-path> [--version V] [--author A]" >&2
  exit 2
fi

TMP="$(mktemp -t opendesk-pack-XXXXXX.yaml)"
trap 'rm -f "${TMP}"' EXIT

if [[ "${SRC}" =~ ^https?:// ]]; then
  echo "[install-pack] downloading ${SRC}"
  curl -fsSL --max-time 60 "${SRC}" -o "${TMP}"
else
  [[ -f "${SRC}" ]] || { echo "[install-pack] not found: ${SRC}" >&2; exit 1; }
  cp "${SRC}" "${TMP}"
fi

echo "[install-pack] validating against the pack schema"
python3 "${ROOT}/scripts/validate_pack.py" validate "${TMP}"

PACK_ID="$(python3 -c 'import sys, yaml; print(yaml.safe_load(open(sys.argv[1]))["id"])' "${TMP}")"
DEST="${INDUSTRIES}/${PACK_ID}.yaml"
cp "${TMP}" "${DEST}"
echo "[install-pack] installed ${DEST}"

python3 "${ROOT}/scripts/validate_pack.py" upsert-index "${INDEX}" "${DEST}" \
  --version "${VERSION}" --author "${AUTHOR}"

echo "[install-pack] WARNING: signature verification is not implemented yet" >&2
echo "[install-pack]   (signature: null recorded; see docs/marketplace.md)" >&2
echo "[install-pack] done: pack '${PACK_ID}' v${VERSION} by ${AUTHOR}"
echo "[install-pack] next: make validate-packs"
