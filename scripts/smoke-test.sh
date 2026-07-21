#!/usr/bin/env bash
# OpenDesk end-to-end smoke test.
# Health checks hit services directly (gateway /api/* routes require a Keycloak JWT);
# public flows go through the APISIX gateway like a real visitor.
# Prereqs: make up && make seed
set -euo pipefail

GW="${GW:-http://localhost:9080}"
SLUG="${SLUG:-acme}"

echo "== 1. Service health (direct ports) =="
for svc in "identity:7001" "booking:7002" "notification:7003" "payments:7004" "edge:7005" "voice:7006" "conversation:7007" "knowledge:7008" "analytics:7009"; do
  name="${svc%%:*}"; port="${svc##*:}"
  curl -sf "http://localhost:${port}/healthz" > /dev/null && echo "$name OK" || { echo "$name FAILED"; exit 1; }
done

echo "== 2. Public tenant context via gateway (agent grounding) =="
curl -sf "$GW/api/bookings/public/sites/$SLUG/context" | jq -e '.site.name // .tenant.slug' > /dev/null && echo "context OK"

echo "== 3. Availability engine via gateway =="
FROM=$(date -u +%Y-%m-%dT00:00:00Z)
curl -sf "$GW/api/bookings/public/sites/$SLUG/availability?from=$FROM" | jq 'length' > /dev/null && echo "availability OK"

echo "== 4. Voice agent text turn via gateway =="
curl -sf -X POST "$GW/voice/chat" \
  -H 'content-type: application/json' \
  -d "{\"site_slug\":\"$SLUG\",\"message\":\"What services do you offer?\"}" | jq -e '.reply' > /dev/null && echo "voice chat OK"

echo "== 5. Ledger balance (direct) =="
curl -sf "http://localhost:7004/v1/accounts/$SLUG/balance" | jq . > /dev/null && echo "payments OK"

echo "== 6. Knowledge search (direct) =="
curl -sf "http://localhost:7008/v1/search?tenant=$SLUG&q=opening%20hours" | jq 'length' > /dev/null && echo "knowledge OK"

echo "== 7. Lakehouse =="
curl -sf "http://localhost:8088/v1/info" > /dev/null && echo "trino OK"
curl -sf "http://localhost:8181/v1/config" > /dev/null && echo "iceberg-rest OK"

echo "ALL SMOKE CHECKS PASSED"
