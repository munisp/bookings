# booking-service

The heart of OpenDesk: catalog, team availability, the slot engine, bookings
with transactional outbox, the booking saga activities and the voice-agent
command consumer (SPEC §4/§6/§7). Go 1.23, chi, pgx/v5, zap, Temporal SDK,
segmentio/kafka-go.

## Endpoints

Tenant API — tenant resolved from `X-Tenant-Slug` header or the JWT
`tenant_slugs` claim (validated by middleware against identity-service via
Dapr invocation); mutating routes check Permify (`manage_catalog` /
`manage_bookings`, subject = JWT `sub` or `X-User-Id`, resource
`organization:{tenantID}`):

| Method | Path | Permify |
|---|---|---|
| GET/POST | `/v1/offerings` | POST: `manage_catalog` |
| GET/PUT/DELETE | `/v1/offerings/{id}` | PUT/DELETE: `manage_catalog` |
| GET/POST | `/v1/team-members` | POST: `manage_catalog` |
| GET/PUT/DELETE | `/v1/team-members/{id}` | PUT/DELETE: `manage_catalog` |
| PUT/GET | `/v1/team-members/{id}/availability` | PUT: `manage_catalog` |
| GET | `/v1/availability?offering_id&team_member_id&from&to` | — |
| GET/PUT | `/v1/site` (auto-creates default on first read; slug immutable; theme jsonb) | PUT: `manage_catalog` |
| GET/POST, GET/PUT/DELETE `/{id}` | `/v1/contacts` | mutations: `manage_bookings` |
| POST/GET | `/v1/bookings` | POST: `manage_bookings` |
| GET | `/v1/bookings/{id}` | — |
| POST | `/v1/bookings/{id}/reschedule`, `/v1/bookings/{id}/cancel` | `manage_bookings` |

Public (no auth; the site slug resolves the tenant server-side — tenant-safe
by construction):

- `GET /public/sites/{slug}/context`
- `GET /public/sites/{slug}/availability?...`
- `POST /public/sites/{slug}/bookings`

Temporal saga activity callbacks (invoked by notification-worker via Dapr):

- `POST /activities/reserve-slot` · `/confirm-booking` · `/release-slot` (compensation) · `/mark-no-show`

Internal: `POST /internal/sites` (seeded by `TenantOnboardingWorkflow`).

Reverse CRM sync (invoked by crm-sync-service via Dapr, tenant via
`X-Tenant-Slug`, no Permify — internal only; **no outbox events**, so the
Twenty → OpenDesk direction can never loop):

- `POST /internal/contacts/upsert` — create-or-update a contact keyed by
  phone OR email within the tenant; stamps `source`/`external_id` (e.g.
  `twenty` / Twenty person id).
- `GET /internal/contacts?phone=|email=` — lookup helper.
- `POST /internal/bookings/{id}/crm-note` — append `{at, source, text}` to
  the booking's `crm_notes` JSONB array (e.g. "Twenty task … marked DONE").

## Behavior notes

- **Phone-confirmation policy**: booking creation (all paths — REST, public,
  Kafka commands) is rejected `422` without a contact phone (SPEC §1/§11).
- **Idempotency**: `idempotency_key` is UNIQUE; replays return the original
  booking. The Kafka consumer uses the CloudEvent `id` as the key.
- **Outbox**: booking mutations insert outbox rows in the same transaction; a
  dispatcher goroutine publishes them as CloudEvents to
  `opendesk.booking.events` via Dapr pubsub `pubsub-kafka` and marks them sent.
- **Saga**: booking creation starts `BookingSagaWorkflow` (workflow ID
  `booking-saga-{bookingID}`, idempotent) on task queue `opendesk-main`;
  bookings stay `pending` until the saga confirms them.
- **Availability engine** (`internal/availability`): pure function over weekly
  rules + existing bookings + buffers + capacity; extensively unit-tested
  (`availability_test.go`: overlap, partial overlap, buffer, capacity,
  effective ranges, timezones, dedup).
- **Kafka command channel**: `opendesk.booking.commands` consumed via a direct
  broker connection (kafka-go, NOT Dapr). Poison messages are dead-lettered to
  `opendesk.dlq` with error metadata after bounded retries; deterministic
  validation errors go straight to the DLQ.

## Environment variables

| Var | Default | Description |
|---|---|---|
| `PORT` | `7002` | HTTP listen port |
| `DATABASE_URL` | — (required) | Postgres DSN for the `booking` DB |
| `PERMIFY_URL` | `http://permify:3476` | Permify HTTP API |
| `AUTHZ_DISABLED` | `false` | Dev escape hatch: skip Permify checks |
| `DAPR_HOST` / `DAPR_HTTP_PORT` | `daprd-booking` / `3500` | daprd sidecar |
| `DAPR_PUBSUB_NAME` | `pubsub-kafka` | Dapr pubsub component |
| `BOOKING_EVENTS_TOPIC` | `opendesk.booking.events` | Outbox topic |
| `IDENTITY_APP_ID` | `identity` | Dapr app-id of identity-service (tenant resolution) |
| `TEMPORAL_HOST_PORT` / `TEMPORAL_NAMESPACE` / `TEMPORAL_TASK_QUEUE` | `temporal:7233` / `opendesk` / `opendesk-main` | Saga client |
| `KAFKA_BROKERS` | `kafka:9092` | Direct broker list for the command consumer |
| `BOOKING_COMMANDS_TOPIC` | `opendesk.booking.commands` | Command topic |
| `BOOKING_COMMANDS_GROUP` | `booking-service-commands` | Consumer group |
| `DLQ_TOPIC` | `opendesk.dlq` | Dead-letter topic |
| `CONSUMER_ENABLED` | `true` | Run the command consumer |
| `OUTBOX_POLL_INTERVAL_SECONDS` | `2` | Outbox poll cadence |
| `SHUTDOWN_TIMEOUT_SECONDS` | `20` | Graceful shutdown budget |

## Schema notes

- Uses SPEC §7 tables: `offerings`, `team_members`, `availability_rules`,
  `contacts`, `bookings`, `outbox`.
- **`sites`** (`id, tenant_id, tenant_slug, slug UNIQUE, display_name,
  published, created_at`) is not defined in SPEC §7 but is required by the
  public booking page; the service creates it idempotently at startup
  (`CREATE TABLE IF NOT EXISTS`). Rows are seeded by the onboarding workflow
  or `POST /internal/sites`.
- **Reverse CRM sync columns** (SPEC-CRM §B): nullable `contacts.source` /
  `contacts.external_id` (+ index on `(tenant_id, external_id)`) and
  `bookings.crm_notes JSONB DEFAULT '[]'` are added idempotently at startup
  (`ALTER TABLE IF EXISTS ... ADD COLUMN IF NOT EXISTS`).

## Auth trust boundary

JWT *signature* verification happens at the APISIX gateway (`jwt-auth` /
`openid-connect` plugins, SPEC §8/§12). booking-service only decodes the
payload claims (`sub`, `tenant_slugs`) inside the cluster network; Permify
enforces per-tenant permissions on every mutation.

## Run

```bash
go build ./... && go test ./...
DATABASE_URL=postgres://opendesk:opendesk@localhost:5432/booking ./server
# or
docker build -t opendesk/booking-service .
```
