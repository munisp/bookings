#!/usr/bin/env bash
# Seed the four industry demo tenants (SPEC-CRM §C5). Uses the direct identity
# port (gateway /api/* requires a JWT). Each tenant is created with its
# industry pack id; the TenantOnboardingWorkflow then applies the pack
# (offerings, knowledge seed, terminology) via ApplyIndustryPack.
set -euo pipefail
IDENTITY="${IDENTITY:-http://localhost:7001}"

create_tenant() {
  local slug="$1" name="$2" industry="$3" tz="$4" currency="$5" locale="$6"
  echo "Seeding tenant $slug (industry: $industry)"
  curl -sf -X POST "$IDENTITY/v1/tenants" -H 'content-type: application/json' -d "{
    \"slug\":\"$slug\",\"name\":\"$name\",\"industry\":\"$industry\",
    \"timezone\":\"$tz\",\"currency\":\"$currency\",\"locale\":\"$locale\",\"plan\":\"pro\"
  }" | jq .
}

create_tenant acme-salon    "Acme Salon & Spa"      salon       "Europe/London" "GBP" "en-GB"
create_tenant acme-clinic   "Acme Health Clinic"    clinic      "Europe/Berlin" "EUR" "de-DE"
create_tenant acme-consult  "Acme Consulting Group" consultancy "America/New_York" "USD" "en-US"
create_tenant acme-support  "Acme Support Desk"     support-desk "UTC"          "USD" "en-US"

echo "Seed complete. Industry packs are applied asynchronously by the TenantOnboardingWorkflow."
