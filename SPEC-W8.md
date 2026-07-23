# SPEC-W8 — Geospatial: PostGIS, MapLibre + GeoLibre, Apache Sedona, Geo-Targeted Campaigns

Wave 8 contract. Repo: `/mnt/agents/output/opendesk` (flaky FUSE — work in `/tmp`, rsync ADDITIVELY, md5-verify).
OWNERSHIP (collision-critical): Agent A owns `docker-compose.yml` + booking-service + postgres init-scripts.
Agent B owns `infra/apisix/apisix.yaml` + `infra/lakehouse/**` + NEW `infra/compose/geolibre.compose.yml` (separate override file — do NOT touch docker-compose.yml). Agent C owns `apps/admin-web/**`. Agent D owns `docs/geospatial.md` + NEW `scripts/seed-geo.sh`.

## Part A — PostGIS location model + geo APIs + geo campaigns (Agent A, Go, booking-service)

### A1. PostGIS enablement
- `docker-compose.yml`: switch postgres image to `postgis/postgis:16-3.4` (drop-in compatible), add
  booking-service env: `GEOCODE_ENABLED=false`, `GEOCODE_BASE_URL=https://nominatim.openstreetmap.org`,
  `GEO_CAMPAIGN_BATCH=50`.
- `infra/postgres/init-scripts/`: `CREATE EXTENSION IF NOT EXISTS postgis;` for the booking database
  (follow existing per-DB pattern).
- Migration (follow booking-service's existing migration pattern):
  - `contact_locations(tenant_id uuid, contact_id uuid, geom geography(Point,4326) NOT NULL,
    source text check (source in ('booking_address','channel_share','manual','geocode')),
    updated_at timestamptz default now(), pk(tenant_id, contact_id))` — RLS like sibling tables
    (withTenant pattern already in the codebase).
  - `service_areas(id uuid pk, tenant_id uuid, name text, geom geography(MultiPolygon,4326) NOT NULL,
    meta jsonb default '{}', created_at)` — RLS.
  - `geo_campaigns(id uuid pk, tenant_id uuid, name text, channel text, message text,
    target geography(MultiPolygon,4326) NOT NULL, audience_count int default 0,
    status text check (status in ('draft','running','completed','failed')) default 'draft',
    created_at timestamptz default now(), started_at timestamptz, completed_at timestamptz)` — RLS.
  - GiST indexes on all geom columns.

### A2. Geo APIs (booking-service, same auth/RLS/tenant patterns as existing routes)
- `PUT /v1/contacts/{id}/location {lat, lng, source?}` — upsert (validate -90..90/-180..180).
- `GET /v1/locations/summary?from=&to=&offering_id=` — booking-joined contact points for the tenant,
  capped at 5000, JSON `{points:[{lat,lng,booking_id,offering_id,starts_at}]}`, plus
  `{clusters:[{lat,lng,count}]}` server-side clustered via ST_SnapToGrid when points > 500.
- `GET /v1/service-areas` / `POST /v1/service-areas {name, geojson, meta}` / `DELETE /v1/service-areas/{id}`
  (accept GeoJSON MultiPolygon/Polygon → ST_GeomFromGeoJSON, promote Polygon→Multi).
- `POST /v1/geo/audience/preview {polygon: <geojson>, }` OR `{center:{lat,lng}, radius_m: N}` →
  `{count, sample:[{contact_id, phone_masked}]}` via ST_Within/ST_DWithin on contact_locations.
- `POST /v1/geo/campaigns {name, channel, message, target:{polygon|radius+center}}` → creates
  geo_campaigns row (running) and starts a NEW Temporal workflow `GeoCampaignWorkflow` (task queue
  opendesk-main): batches audience contacts (GEO_CAMPAIGN_BATCH), for each recipient calls the EXISTING
  paced notification path (NotifyPaced — reuse exactly like other paced kinds; add kind
  `geo_campaign` to the paced kinds set), updates audience_count/status. Message personalization token
  `{name}` supported. Heartbeats + idempotent replay (skip contacts already sent for this campaign id).
- `GET /v1/geo/campaigns` / `GET /v1/geo/campaigns/{id}` — list/status.
- Emit usage event metric `geo_campaign_message` (value=1 per recipient) on opendesk.usage.events via the
  existing UsageExtra outbox pattern, so geo campaigns are metered/billed.
- Optional geocoding hook: when GEOCODING_ENABLED=true and a booking carries an address string, geocode via
  Nominatim (1 req/s, descriptive User-Agent, 5s timeout, cache by address hash) → contact location
  source=`geocode`. Off by default.

### A3. Tests (go test must pass)
ST validation helpers (bbox validation, polygon geojson → WKT errors), audience preview SQL builder
(unit-level with sqlmock or pure-builder tests — follow repo's existing store test approach), campaign
workflow activity unit tests (batching, idempotent skip, {name} token), usage event emission.
`go build ./... && go vet ./... && go test ./...` green in booking-service (Go at /tmp/sdk/go/bin/go or
reinstall to /tmp/sdk: curl -sSL https://dl.google.com/go/go1.23.4.linux-amd64.tar.gz | tar -C /tmp/sdk -xzf-;
GOPROXY=https://goproxy.cn,direct).

## Part B — Apache Sedona lakehouse + GeoLibre workbench (Agent B, Python/Spark/infra)

### B1. Sedona in the lakehouse
- Read infra/lakehouse/spark/ (existing jobs: revenue_intelligence.py etc.) and follow its patterns.
- Add Sedona to the Spark environment: `org.apache.sedona:sedona-spark-shaded-3.5_2.12:1.7.0` (match the
  repo's Spark version — CHECK the existing Dockerfile/conf first and use the matching sedona-spark
  variant) + `org.datasyslab:geotools-wrapper:1.7.0-28.5`; SedonaContext builder in a shared
  `infra/lakehouse/spark/jobs/sedona_common.py`.
- NEW job `infra/lakehouse/spark/jobs/geo_analytics.py`:
  - Read silver bookings + contact location extracts (GeoParquet in MinIO bronze/silver or JDBC —
    follow how existing jobs read their inputs).
  - Produce gold tables (Iceberg, Trino-visible like existing gold):
    - `gold.geo_demand_h3` — bookings per H3 res-8 cell per tenant per day (ST_H3 or geohash fallback if
      H3 unavailable in the pinned Sedona version — document choice), with cell geometry.
    - `gold.geo_service_area_coverage` — bookings inside vs outside each service area (ST_Within join
      against a service-areas extract).
    - `gold.geo_hourly_density` — demand heat cells by hour-of-week for staffing.
- dbt: add gold geo models + docs following infra/lakehouse/dbt patterns.

### B2. GeoLibre workbench
- NEW `infra/compose/geolibre.compose.yml` (separate override): `ghcr.io/opengeos/geolibre:latest`,
  port 8085, restart policy, GEOLIBRE_DISABLE_SIDECAR=1 default with comment on enabling the sidecar.
- `infra/apisix/apisix.yaml`: route `/gis/*` → geolibre:8085 behind the file's standard jwt pattern
  (strip prefix per existing rewrite examples).
- `infra/lakehouse/notebooks/geolibre-exploration.ipynb` — analyst notebook: `pip install geolibre`,
  render gold.geo_demand_h3 GeoJSON + service areas on a GeoLibre Map widget (leafmap-style API:
  Map(center=...), add_geojson(...)), save .geolibre.json project example. Include markdown explaining
  the Sedona → gold → GeoLibre flow.
- `docs/geospatial-infra.md` — Sedona setup notes, gold table reference, GeoLibre deployment/embedding
  (`maponly` embed mode note for admin dashboards), Trino geo queries cookbook.

## Part C — Admin map dashboards + geo targeting (Agent C, TypeScript, admin-web)

- New deps allowed (package.json): `maplibre-gl` (^4) and `@maplibre/maplibre-gl-draw` (^3). npm ci +
  npx tsc --noEmit must pass.
- NEW `app/app/[orgSlug]/locations/` page ("Locations" nav entry, visible to owner/admin/staff — reuse
  lib/roles.ts patterns; add role helper only if needed):
  - MapLibre map (OSM raster basemap with attribution; style inline, no external token) rendering
    `GET /api/bookings/v1/locations/summary` points/clusters (circle layers + cluster click zoom),
    date-range + offering filters, service-area polygons from `GET /v1/service-areas` (toggleable layer),
    create/delete service areas by drawing a polygon (maplibre-gl-draw) → POST.
- NEW `app/app/[orgSlug]/geo-campaigns/` page ("Geo campaigns" nav, owner/admin only):
  - Draw circle (center+radius slider) or polygon → `POST /v1/geo/audience/preview` live count.
  - Compose message ({name} token hint), pick channel (whatsapp/telegram/sms — reflect tenant's
    configured channels), launch → `POST /v1/geo/campaigns`.
  - Campaign table: name, audience_count, status, created_at (poll while running).
- Empty/error states graceful (no locations yet → hint card explaining location capture).
- Warm low-saturation style consistent with existing pages; map container dark-mode safe.

## Part D — 30-vertical geospatial use cases (Agent D, docs)

- `docs/geospatial.md`: (1) architecture overview (PostGIS operational + Sedona analytical + MapLibre/
  GeoLibre visualization + geo campaigns), (2) a section PER vertical pack for ALL 30 packs in
  industries/index.json — each: 2–3 concrete geospatial analytics use cases, the exact gold table /
  API / campaign feature that powers each, and an example (e.g. logistics: failed-delivery heat cells →
  reroute hubs → gold.geo_demand_h3; civic-services: pothole report clusters → crew dispatch priority →
  geo campaign to affected residents; pharmacy: refill-reminder geo campaigns within delivery radius).
  Cover every pack id from the registry — no skips.
- NEW `scripts/seed-geo.sh`: seeds 2 demo service areas + ~50 synthetic contact locations around Lagos
  (6.5244, 3.3792) and London (51.5074, -0.1278) for demo tenants via the new APIs (curl, follows
  seed-industries.sh conventions, idempotent-ish, documented env overrides).
- README.md: add a "Geospatial" bullet row linking docs/geospatial.md (ONE line, keep diff minimal).

## Cross-agent API contract (A builds, C consumes — exact paths under booking-service BFF /api/bookings)
- `GET /api/bookings/v1/locations/summary?from&to&offering_id` → `{points:[...], clusters:[...]}`
- `GET|POST /api/bookings/v1/service-areas`, `DELETE /api/bookings/v1/service-areas/{id}`
- `POST /api/bookings/v1/geo/audience/preview` → `{count, sample:[...]}`
- `POST /api/bookings/v1/geo/campaigns`, `GET /api/bookings/v1/geo/campaigns[/{id}]`
