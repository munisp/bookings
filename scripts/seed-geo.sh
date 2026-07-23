#!/usr/bin/env bash
# Seed demo geospatial data (SPEC-W8 Part D). Uses the direct booking-service
# port (gateway /api/* requires a JWT); booking-service runs with
# AUTHZ_DISABLED=true in dev, so X-Tenant-Slug selects the tenant — same
# convention as scripts/seed-demo.sh.
#
# What it seeds:
#   * 2 demo service areas (GeoJSON polygons):
#       - "Lagos Island Demo Zone"  on the Lagos tenant  (~6.45, 3.39)
#       - "London Zones 1-2 Demo"   on the London tenant (~51.5074, -0.1278)
#   * ~50 synthetic contact locations via PUT /v1/contacts/{id}/location:
#       - 25 with deterministic jitter around Lagos  (6.5244, 3.3792)
#       - 25 with deterministic jitter around London (51.5074, -0.1278)
#
# IMPORTANT: contacts must already exist. Run scripts/seed-industries.sh
# FIRST (it creates the demo tenants), and make sure the tenants have at
# least one booking/contact (or let this script create synthetic
# "Geo Demo Customer" contacts when a tenant has none — it does so
# automatically). If the tenant itself is missing, the script fails with a
# helpful message.
#
# Idempotent-ish: existing service areas with the same name are skipped, and
# location upserts are safe to re-run (PUT is an upsert; jitter is
# deterministic so re-runs write the same points).
#
# Env overrides:
#   BOOKING        booking-service base URL   (default http://localhost:7002)
#   LAGOS_TENANT   tenant slug for Lagos data (default acme-ng)
#   LONDON_TENANT  tenant slug for London data (default acme-salon)
#   PER_CITY       contacts geocoded per city (default 25)
set -euo pipefail
BOOKING="${BOOKING:-http://localhost:7002}"
LAGOS_TENANT="${LAGOS_TENANT:-acme-ng}"
LONDON_TENANT="${LONDON_TENANT:-acme-salon}"
PER_CITY="${PER_CITY:-25}"

command -v jq  >/dev/null || { echo "ERROR: jq is required" >&2; exit 1; }
command -v awk >/dev/null || { echo "ERROR: awk is required" >&2; exit 1; }

# Deterministic pseudo-random jitter: frac in [0,1) derived from index i.
# Same i always yields the same offset, so re-runs converge to the same data.
jitter() { # jitter <index> <base> <span_deg>
  awk -v i="$1" -v base="$2" -v span="$3" 'BEGIN{
    s=sin(i*12.9898+78.233)*43758.5453; f=s-int(s);
    printf "%.6f", base+(f-0.5)*span }'
}

create_service_area() { # create_service_area <tenant> <name> <geojson>
  local tenant="$1" name="$2" geojson="$3"
  local existing
  existing=$(curl -sf "$BOOKING/v1/service-areas" -H "X-Tenant-Slug: $tenant" \
    | jq -r --arg n "$name" '[(.service_areas // .areas // [])[] | select(.name==$n) | .id][0] // empty' || true)
  if [ -n "$existing" ]; then
    echo "  service area '$name' already exists on $tenant — skipping"
    return
  fi
  echo "  creating service area '$name' on $tenant"
  curl -sf -X POST "$BOOKING/v1/service-areas" -H "X-Tenant-Slug: $tenant" \
    -H 'content-type: application/json' \
    -d "{\"name\":\"$name\",\"geojson\":$geojson,\"meta\":{\"source\":\"seed-geo.sh\",\"demo\":true}}" | jq -c .
}

seed_city() { # seed_city <tenant> <base_lat> <base_lng> <label>
  local tenant="$1" blat="$2" blng="$3" label="$4"
  echo "Seeding $PER_CITY contact locations around $label ($blat, $blng) on tenant $tenant"

  local resp
  if ! resp=$(curl -sf "$BOOKING/v1/contacts" -H "X-Tenant-Slug: $tenant"); then
    echo "ERROR: could not list contacts for tenant '$tenant'." >&2
    echo "       Run scripts/seed-industries.sh first to create the demo tenants," >&2
    echo "       or set LAGOS_TENANT/LONDON_TENANT to an existing tenant slug." >&2
    exit 1
  fi

  local ids
  ids=$(echo "$resp" | jq -r '.contacts[]?.id')
  if [ -z "$ids" ]; then
    echo "  no contacts found on $tenant — creating $PER_CITY synthetic demo contacts"
    local i
    for i in $(seq 1 "$PER_CITY"); do
      curl -sf -X POST "$BOOKING/v1/contacts" -H "X-Tenant-Slug: $tenant" \
        -H 'content-type: application/json' \
        -d "{\"name\":\"Geo Demo Customer $i ($label)\",\"phone\":\"+1000000$(printf '%03d' "$i")\",\"notes\":\"synthetic contact created by scripts/seed-geo.sh\"}" >/dev/null
    done
    ids=$(curl -sf "$BOOKING/v1/contacts" -H "X-Tenant-Slug: $tenant" | jq -r '.contacts[]?.id')
  fi

  local n=0 id lat lng
  for id in $ids; do
    [ "$n" -ge "$PER_CITY" ] && break
    lat=$(jitter "$n" "$blat" "0.09")   # ~±5 km lat
    lng=$(jitter $((n + 1000)) "$blng" "0.12") # ~±6 km lng
    curl -sf -X PUT "$BOOKING/v1/contacts/$id/location" -H "X-Tenant-Slug: $tenant" \
      -H 'content-type: application/json' \
      -d "{\"lat\":$lat,\"lng\":$lng,\"source\":\"manual\"}" >/dev/null
    n=$((n + 1))
  done
  echo "  upserted $n contact locations on $tenant"
}

# Lagos Island polygon (GeoJSON: [lng, lat]) around 6.45, 3.39.
LAGOS_GEOJSON='{"type":"Polygon","coordinates":[[[3.360,6.440],[3.425,6.438],[3.435,6.470],[3.380,6.485],[3.358,6.462],[3.360,6.440]]]}'
# London Zones 1-2 polygon around 51.5074, -0.1278.
LONDON_GEOJSON='{"type":"Polygon","coordinates":[[[-0.190,51.480],[-0.060,51.480],[-0.055,51.535],[-0.200,51.535],[-0.190,51.480]]]}'

echo "Seeding service areas"
create_service_area "$LAGOS_TENANT"  "Lagos Island Demo Zone" "$LAGOS_GEOJSON"
create_service_area "$LONDON_TENANT" "London Zones 1-2 Demo"  "$LONDON_GEOJSON"

seed_city "$LAGOS_TENANT"  "6.5244"  "3.3792"  "Lagos"
seed_city "$LONDON_TENANT" "51.5074" "-0.1278" "London"

echo "Geo seed complete. Open the admin Locations page (or /gis) to explore,"
echo "and GET $BOOKING/v1/locations/summary -H 'X-Tenant-Slug: $LAGOS_TENANT' for points/clusters."
