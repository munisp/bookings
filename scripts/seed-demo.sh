#!/usr/bin/env bash
# Seed demo tenant "acme". Uses direct service ports (gateway /api/* requires a JWT).
# booking-service runs with AUTHZ_DISABLED=true in dev (see root compose) so no
# Keycloak token is needed; X-Tenant-Slug selects the tenant.
set -euo pipefail
IDENTITY="${IDENTITY:-http://localhost:7001}"
BOOKING="${BOOKING:-http://localhost:7002}"
KNOWLEDGE="${KNOWLEDGE:-http://localhost:7008}"
SLUG=acme

echo "Seeding tenant $SLUG"
curl -sf -X POST "$IDENTITY/v1/tenants" -H 'content-type: application/json' -d '{
  "slug":"acme","name":"Acme Studio","timezone":"Europe/London",
  "currency":"GBP","locale":"en-GB","plan":"pro",
  "terminology":{"offering":"service","team_member":"stylist","booking":"appointment","contact":"client"}
}' | jq .

echo "Seeding offerings"
curl -sf -X POST "$BOOKING/v1/offerings" -H "X-Tenant-Slug: $SLUG" -H 'content-type: application/json' -d '{"name":"Haircut","duration_min":30,"buffer_min":10,"price_cents":3500,"currency":"GBP","capacity":1,"bookable":true}' | jq .
curl -sf -X POST "$BOOKING/v1/offerings" -H "X-Tenant-Slug: $SLUG" -H 'content-type: application/json' -d '{"name":"Consultation","duration_min":45,"buffer_min":15,"price_cents":6000,"currency":"GBP","capacity":1,"bookable":true}' | jq .

echo "Seeding knowledge"
curl -sf -X POST "$KNOWLEDGE/v1/documents" -H 'content-type: application/json' -d "{\"tenant\":\"$SLUG\",\"title\":\"Opening hours & policies\",\"body\":\"Open Mon-Sat 9:00-18:00. Cancellations free up to 24h before the appointment, otherwise a no-show fee applies.\"}" | jq .

echo "Seed complete."
