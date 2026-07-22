# booking-service (Go 1.23, chi, port 7002)

Core transactional service: offerings/team/availability/contacts/bookings CRUD,
the availability engine, public booking-page endpoints, and Temporal saga
activity callbacks (SPEC §2, §6).

## API surface

- `GET /healthz`
- Tenant-scoped (`X-Tenant-Slug` header, JWT `tenant_slugs` enforcement in prod):
  - `GET/POST /v1/offerings`, `GET/PUT/DELETE /v1/offerings/{id}`
  - `GET/POST /v1/team-members`, `GET/PUT/DELETE /v1/team-members/{id}`
  - `GET/PUT /v1/team-members/{id}/availability`
  - `GET /v1/availability?offering_id=&team_member_id=&from=&to=`
  - `GET/POST /v1/contacts`, `GET/PUT/DELETE /v1/contacts/{id}`
  - `POST /v1/bookings`, `GET /v1/bookings`, `GET /v1/bookings/{id}`
  - `POST /v1/bookings/{id}/reschedule`, `POST /v1/bookings/{id}/cancel`
- Public (no auth, site-slug scoped):
  - `GET /public/sites/{slug}/context`
  - `GET /public/sites/{slug}/availability?offering_id=&team_member_id=&from=&to=`
  - `POST /public/sites/{slug}/bookings`
- Internal (Dapr service invocation):
  - `POST /internal/sites` (tenant onboarding)
  - `POST /activities/reserve-slot`, `POST /activities/confirm-booking`,
    `POST /activities/release-slot`, `POST /activities/mark-no-show`

## Policies

- **Phone-confirmation policy** (SPEC §1): booking mutations require a contact
  with a phone number; otherwise `422`.
- **Idempotency**: `idempotency_key` on create; conflicts return the original
  booking (`409` on conflicting replay with different payload).
- **Outbox**: domain events (`BookingCreated/Confirmed/Rescheduled/Cancelled/NoShow`)
  are written to the `outbox` table in the same tx and drained to Kafka
  `opendesk.booking.events` (CloudEvents 1.0) by an in-process dispatcher.
- **RLS** (SPEC-W3 §2): every tenant-scoped query runs inside a tx that first
  executes `SET LOCAL app.tenant_id = '<tenant uuid>'` so Postgres row-level
  security enforces isolation. Exceptions (documented in code): bootstrap DDL,
  the cross-tenant outbox dispatcher, and public site-slug resolution.
- **AuthZ**: Permify checks (`manage_catalog` / `manage_bookings`) on mutations;
  `AUTHZ_DISABLED=true` in dev compose.

## Config (env)

| Var | Default | Purpose |
|---|---|---|
| `PORT` | `7002` | HTTP bind |
| `DATABASE_URL` | `postgres://opendesk:opendesk@postgres:5432/booking?sslmode=disable` | Postgres DSN (compose sets `BOOKING_PG_USER`/`BOOKING_PG_PASS` variants) |
| `DAPR_HTTP_PORT` | `3500` | Dapr sidecar HTTP port |
| `DAPR_PUBSUB` | `pubsub-kafka` | Dapr pubsub component name (events publish) |
| `AUTHZ_DISABLED` | `false` | Skip Permify checks (dev) |
| `PERMIFY_HOST` | `permify:3478` | Permify gRPC endpoint |
| `TEMPORAL_HOST` | `temporal:7233` | Temporal frontend (saga start) |
| `TEMPORAL_NAMESPACE` | `opendesk` | Temporal namespace |
| `TEMPORAL_TASK_QUEUE` | `opendesk-main` | Task queue for `BookingSagaWorkflow` |
| `IDENTITY_APP_ID` | `identity` | Dapr app-id for tenant context lookups |
| `DLQ_TOPIC` | `opendesk.dlq` | Dead-letter topic for the outbox dispatcher |

## Run

```bash
go build ./... && go test ./...
PORT=7002 DATABASE_URL=postgres://... ./booking-service
```
