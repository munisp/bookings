#!/bin/sh
# load-schema.sh — one-shot Permify schema loader (SPEC §8).
# POSTs infra/permify/schema.perm to the Permify HTTP write-schema endpoint
# for the bootstrap tenant (TENANT_ID, default t1).
set -e

PERMIFY_HTTP="${PERMIFY_HTTP:-http://permify:3476}"
TENANT_ID="${TENANT_ID:-t1}"
SCHEMA_FILE="${SCHEMA_FILE:-/loader/schema.perm}"

echo "[permify-loader] waiting for ${PERMIFY_HTTP} ..."
i=0
until curl -sf "${PERMIFY_HTTP}/healthz" >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 60 ]; then
    echo "[permify-loader] permify not reachable" >&2
    exit 1
  fi
  sleep 2
done

echo "[permify-loader] writing schema to tenant ${TENANT_ID}"
# Escape the schema into a JSON string payload.
payload=$(awk 'BEGIN{printf "{\"schema\":\""} {gsub(/\\/,"\\\\"); gsub(/"/,"\\\""); printf "%s\\n", $0} END{printf "\"}"}' "${SCHEMA_FILE}")

curl -sf -X POST "${PERMIFY_HTTP}/v1/tenants/${TENANT_ID}/schemas/write" \
  -H "Content-Type: application/json" \
  -d "${payload}"

echo
echo "[permify-loader] schema loaded:"
curl -sf -X POST "${PERMIFY_HTTP}/v1/tenants/${TENANT_ID}/schemas/list" \
  -H "Content-Type: application/json" -d '{}'
echo
echo "[permify-loader] done"
