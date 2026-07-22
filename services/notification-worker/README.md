# notification-worker

Temporal Go worker hosting the durable workflows of SPEC §6 plus the Go
activities that reach booking/payments/identity via Dapr service invocation
and send notifications through the Dapr output bindings. Go 1.23, Temporal
SDK, zap.

## Workflows (namespace `opendesk`, task queue `opendesk-main`)

| Workflow | Behavior |
|---|---|
| `BookingSagaWorkflow` | `ReserveSlot` → `HoldDeposit` (priced offerings only) → `ConfirmBooking` → `SendConfirmation`. Explicit compensation stack executed in **reverse order**: `VoidHold` → `ReleaseSlot`, on a disconnected context so compensation survives cancellation. `cancel` signal triggers compensation; `state` query reports progress. On success it starts `ReminderWorkflow` + `NoShowFollowupWorkflow` as children. |
| `ReminderWorkflow` | Timers at T-24h and T-1h. `booking-event` signal: `cancelled` stops, `rescheduled` re-arms remaining timers. Re-checks booking status via `GetBookingStatus` before every send. |
| `NoShowFollowupWorkflow` | Waits until appointment end + 2h grace, checks status, marks `no_show` via `MarkNoShow`, sends follow-up. |
| `TenantOnboardingWorkflow` | Idempotent steps in order: Keycloak group (`EnsureKeycloakGroup`), Permify tenant (`EnsurePermifyTenant`), Postgres seed = default public site row (`SeedTenantData`), OpenSearch alias `kb-{slug}` (`EnsureSearchAlias`). |

All activities use `ActivityOptions` with `StartToCloseTimeout=30s`, heartbeat
10s and a retry policy (1s initial, ×2 backoff, max 3 attempts).

## HTTP endpoints

| Method | Path | Description |
|---|---|---|
| GET | `/healthz` | Liveness |
| POST | `/dev/trigger-reminder` | Start a `ReminderWorkflow` with short delays (`delays_seconds`, default `[5,10]`) for manual testing |
| POST | `/dev/trigger-onboarding` | Start a `TenantOnboardingWorkflow` manually |
| POST | `/v1/signals` | Deliver a signal to a running workflow. Body: `{"workflow_id","signal","payload"?}`. Payload is optional (`IntakeCompleted`, `Responded`, `NoShow` carry none; `booking-event` takes `{"type":"cancelled"}`). Returns 202, 404 when the workflow is not running. Used by staff UIs to mark clinic intake completed / support tickets responded on the `pack-{bookingId}` workflows (SPEC-CRM §C2). |

## Signal bridge (SPEC-CRM §C2)

A Kafka consumer goroutine (`internal/signals`, topic `opendesk.booking.events`,
group `notification-signals`) forwards booking lifecycle events to the
per-booking child workflows started by the saga (`ParentClosePolicy=ABANDON`,
so they outlive the saga):

- `BookingCancelled` → `booking-event` `{type:"cancelled"}` to `pack-{bookingId}` and `reminder-{bookingId}`
- `BookingNoShow` → `NoShow` to `pack-{bookingId}`

Delivery is best-effort (workflows re-check booking state via activities);
workflows that are not running are logged and acknowledged — never retried,
never dead-lettered. Config: `KAFKA_BROKERS`, `BOOKING_EVENTS_TOPIC`,
`SIGNAL_GROUP`.

## Notifications

`SendConfirmation` / `SendReminder` / `SendNoShowFollowup` render templated
bodies (text/template) and invoke Dapr output bindings with operation
`create`:

- `bindings-smtp` — metadata `emailTo`, `emailFrom`, `subject`
- `bindings-twilio` — metadata `toNumber`, `fromNumber`

Channels without a recipient (empty email/phone) are skipped.

## Environment variables

| Var | Default | Description |
|---|---|---|
| `PORT` | `7003` | HTTP sidecar port |
| `TEMPORAL_HOST_PORT` | `temporal:7233` | Temporal frontend |
| `TEMPORAL_NAMESPACE` | `opendesk` | Namespace |
| `TEMPORAL_TASK_QUEUE` | `opendesk-main` | Task queue |
| `DAPR_HOST` / `DAPR_HTTP_PORT` | `daprd-notification` / `3500` | daprd sidecar |
| `BOOKING_APP_ID` | `booking` | Dapr app-id of booking-service |
| `PAYMENTS_APP_ID` | `payments` | Dapr app-id of payments-service |
| `IDENTITY_APP_ID` | `identity` | Dapr app-id of identity-service |
| `SMTP_BINDING` | `bindings-smtp` | Email output binding |
| `TWILIO_BINDING` | `bindings-twilio` | SMS output binding |
| `SMTP_FROM` | `no-reply@opendesk.local` | Sender address |
| `TWILIO_FROM` | `+10000000000` | Sender number |
| `OPENSEARCH_URL` | `http://opensearch:9200` | For the onboarding search-alias activity |
| `SHUTDOWN_TIMEOUT_SECONDS` | `20` | Graceful shutdown budget |

## Payments contract

`HoldDeposit` → `POST /activities/hold-deposit` `{booking_id, tenant_id,
amount_cents, currency}` returning `{hold_id}`;
`VoidHold` → `POST /activities/void-hold` `{hold_id, booking_id, tenant_id}`.
The saga threads `hold_id` from the hold response into the compensation; the
hold step is skipped for `price_cents=0` offerings (TigerBeetle transfer codes
per SPEC §9 are applied inside payments-service).

## Tests

`internal/workflows/booking_saga_test.go` uses the Temporal testsuite:
happy-path ordering, reverse-order compensation (`VoidHold` → `ReleaseSlot`)
on `ConfirmBooking` failure, and no compensation when `ReserveSlot` fails.

## Run

```bash
go build ./... && go test ./...
./worker
# or
docker build -t opendesk/notification-worker .
```
