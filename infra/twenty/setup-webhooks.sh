#!/usr/bin/env bash
# setup-webhooks.sh — register the OpenDesk reverse-sync webhook in Twenty,
# idempotently (SPEC-CRM §B: Twenty -> OpenDesk direction).
#
# What it does:
#   1. GET  {TWENTY_API_URL}/rest/webhooks  (Bearer TWENTY_API_KEY)
#      — if a webhook with our targetUrl already exists, exit 0 (idempotent).
#   2. POST {TWENTY_API_URL}/rest/webhooks
#      { "targetUrl": "http://crm-sync:7010/webhooks/twenty",
#        "operations": ["person.created","person.updated","task.updated"],
#        "secret":     "$TWENTY_WEBHOOK_SECRET" }
#
# NOTE (version-sensitive): field names follow Twenty's v1 REST webhook
# object schema (targetUrl / operations / secret) as of twenty v1.x — verify
# against your version's docs (https://twenty.com/developers, section
# "Webhooks" / the /rest/webhooks endpoint) before running against an
# upgraded Twenty. In particular:
#   - operation names are "<object>.<action>" (e.g. "person.created"); some
#     versions also accept the wildcard "*";
#   - recent Twenty versions can generate their own per-webhook signing
#     secret (Settings -> API & Webhooks): whatever secret Twenty signs
#     X-Twenty-Webhook-Signature with MUST equal crm-sync's
#     TWENTY_WEBHOOK_SECRET, or intake will 401.
#
# Env:
#   TWENTY_API_URL          default http://localhost:3100 (host port-mapped;
#                           use http://twenty-api:3000 from inside compose)
#   TWENTY_API_KEY          required — Settings -> API & Webhooks -> API keys
#   WEBHOOK_TARGET_URL      default http://crm-sync:7010/webhooks/twenty
#                           (the in-compose address of crm-sync; Twenty runs
#                           in the same docker network)
#   TWENTY_WEBHOOK_SECRET   default opendesk-dev-twenty-webhook-secret
#                           (matches the compose dev fallback)
#   TWENTY_WEBHOOK_OPERATIONS  default person.created,person.updated,task.updated
set -euo pipefail

TWENTY_API_URL="${TWENTY_API_URL:-http://localhost:3100}"
TWENTY_API_KEY="${TWENTY_API_KEY:-}"
WEBHOOK_TARGET_URL="${WEBHOOK_TARGET_URL:-http://crm-sync:7010/webhooks/twenty}"
TWENTY_WEBHOOK_SECRET="${TWENTY_WEBHOOK_SECRET:-opendesk-dev-twenty-webhook-secret}"
OPERATIONS="${TWENTY_WEBHOOK_OPERATIONS:-person.created,person.updated,task.updated}"

if [ -z "$TWENTY_API_KEY" ]; then
  echo "error: TWENTY_API_KEY is required (Twenty Settings -> API & Webhooks)" >&2
  exit 1
fi

auth=(-H "Authorization: Bearer ${TWENTY_API_KEY}" -H "Content-Type: application/json")

echo ">> Checking existing webhooks at ${TWENTY_API_URL}/rest/webhooks ..."
existing="$(curl -fsS "${auth[@]}" "${TWENTY_API_URL}/rest/webhooks")"
if printf '%s' "$existing" | grep -qF "$WEBHOOK_TARGET_URL"; then
  echo ">> Webhook for ${WEBHOOK_TARGET_URL} already registered — nothing to do."
  exit 0
fi

# Build the JSON operations array from the comma-separated list.
ops_json="$(printf '%s' "$OPERATIONS" | awk -F',' '{ for (i=1; i<=NF; i++) printf "%s\"%s\"", (i>1?",":""), $i }')"

body="$(cat <<JSON
{"targetUrl": "${WEBHOOK_TARGET_URL}", "operations": [${ops_json}], "secret": "${TWENTY_WEBHOOK_SECRET}"}
JSON
)"

echo ">> Creating webhook -> ${WEBHOOK_TARGET_URL} [${OPERATIONS}] ..."
curl -fsS -X POST "${auth[@]}" -d "$body" "${TWENTY_API_URL}/rest/webhooks"
echo
echo ">> Done. Ensure crm-sync runs with TWENTY_WEBHOOK_SECRET=${TWENTY_WEBHOOK_SECRET}"
echo ">> Verify: curl -fsS -H \"Authorization: Bearer \$TWENTY_API_KEY\" ${TWENTY_API_URL}/rest/webhooks"
