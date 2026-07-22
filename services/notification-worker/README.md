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
| POST/GET | `/v1/webhooks` | Outbound webhook subscriptions (Wave 5 #10). Tenant via `X-Tenant-Slug` (resolved through identity-service). POST body `{"url","events":[],"secret"?}` — events support exact types, `prefix.*` and `*`; the secret is generated when omitted and returned **only in the create response** (list masks it as `secret_set`). |
| DELETE | `/v1/webhooks/{id}` | Remove a subscription (deliveries cascade). |
| GET | `/v1/webhooks/{id}/deliveries` | Delivery history (status `pending`/`retrying`/`delivered`/`dlq`, attempts, last_status_code, next_retry_at). |

APISIX fronts these at `/api/notifications/*` with the standard jwt +
rewrite pattern (upstream `notification:7003`).

## Outbound webhook platform (Wave 5 #10)

`opendesk.booking.events` + `opendesk.conversation.events` are consumed by
the webhook dispatcher (kafka-go, group `notification-webhooks`). Every
event is matched against the tenant's active subscriptions
(`webhooks.EventMatches`: exact / `prefix.*` / `*`); each match inserts a
`webhook_deliveries` row and starts one `WebhookDeliveryWorkflow` (id
`webhook-delivery-{deliveryId}`, so redelivered events are idempotent).

The workflow POSTs the raw CloudEvents envelope with headers
`X-OpenDesk-Signature: sha256=<hex HMAC-SHA256(secret, body)>`,
`X-OpenDesk-Event`, `X-OpenDesk-Timestamp`, `X-OpenDesk-Delivery`, then
retries on the durable-timer schedule **1m, 5m, 15m, 1h, 4h** (up to 6
attempts), persisting `retrying` (+`next_retry_at`) after each failure and
`delivered`/`dlq` at the end. `WEBHOOK_SIGNING_REQUIRED=true` rejects
secret-less subscriptions (dev default `false` — deliveries are still
signed whenever a secret exists).

Tables `webhook_subscriptions` / `webhook_deliveries` live in the
`notifications` database (`DATABASE_URL`; bootstrapped idempotently at
startup — without it the platform degrades to 503 on `/v1/webhooks` and no
dispatcher, the rest of the worker is unaffected).

## Notifications outbox consumer (Wave 5 #7)

`opendesk.notifications.outbox` (group `notification-outbox`) carries
fire-and-forget notification commands. booking-service publishes
`com.opendesk.notifications.SendPortalCode` when a customer requests a
portal login code; this consumer delivers it through the same smtp/twilio
bindings as the workflow activities (the plaintext code exists only in the
event payload and the customer message — booking stores its SHA-256 hash).

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

- `bindings-smtp` — metadata `emailTo`, `emailFrom`, `subject`, `senderNumber`
- `bindings-twilio` — metadata `toNumber`, `fromNumber`, `senderNumber`

Channels without a recipient (empty email/phone) are skipped.

## Outbound CPS pacing & sender rotation

(docs/VOICE-SCALING.md §4 telephony plane. **Full coverage**: every
workflow-driven outbound send is CPS-paced — booking confirmations, T-24h /
T-1h reminders, no-show follow-ups, waitlist claim links, and all industry
pack sends: salon deposit reminders, clinic intake reminders, consultancy
follow-up + proposal reminders, and support-desk SLA-breach escalations.)

The carrier sets two ceilings, not us: **channel count** (hard cap of
simultaneous calls on the SIP trunk) and **CPS** (call/message *start
rate*). CPS binds outbound campaigns first, and pacing is one knob for both
CPS compliance and **spam reputation** — a smooth low start rate is exactly
what keeps sender numbers off carrier spam lists. Outbound sends from
workflows therefore never call the binding activities directly; they call
the single `NotifyPaced` activity, which:

1. **Acquires a CPS token** (activity-side, so workflows stay
   deterministic) from a token bucket: `OUTBOUND_CPS` tokens/sec, capacity
   `OUTBOUND_BURST`. With `PACER_BACKEND=redis` (default) the bucket is a
   Lua script in the shared `redis:6379` — this is the only correct choice
   with more than one worker replica, since a per-process limiter would
   silently multiply fleet-wide CPS by the replica count.
   `PACER_BACKEND=local` uses a `golang.org/x/time/rate` limiter shared by
   all activities in the process (single-replica dev only).
2. **Rotates the sender** round-robin through `OUTBOUND_FROM_NUMBERS`
   (comma list). Redis backend: shared `INCR` counter so rotation
   interleaves fairly across replicas; local backend: process atomic. The
   chosen number becomes the Twilio `fromNumber` and is recorded as
   `senderNumber` in both binding payloads' metadata for reputation
   tracing. Empty pool → the configured `TWILIO_FROM` default is used.

**Fail-open policy**: when redis is unreachable the pacer logs one warning
and falls back to the local limiter/counter rather than dropping sends —
claim links and reminders are time-sensitive, and each replica still paces
itself locally (worst case replicas × CPS, never an unbounded burst). The
redis backend is retried on every send and resumes automatically.

**SIP trunk notes** (for when LiveKit SIP lands, `deploy/` trunk config):
channel count and CPS are *procurement* items — raising either means
talking to the carrier, not editing config. Size: `channels >= peak
concurrent calls`, `OUTBOUND_CPS <= carrier CPS per trunk` (and per
sending-number reputation tier); scale out by adding trunks/numbers and
regional origination, then raise `OUTBOUND_FROM_NUMBERS` before
`OUTBOUND_CPS`. The same pacer will gate SIP dials: it sits *before* the
dial exactly as it now sits before the binding send.

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
| `OUTBOUND_CPS` | `1.0` | Outbound start rate (sends/sec) — carrier CPS + spam-reputation knob |
| `OUTBOUND_BURST` | `3` | Token-bucket capacity (max instant sends after idle) |
| `PACER_BACKEND` | `redis` | `redis` = fleet-wide Lua bucket, `local` = in-process (single replica) |
| `OUTBOUND_FROM_NUMBERS` | _(empty)_ | Comma-separated sender rotation pool; empty keeps `TWILIO_FROM` |
| `REDIS_ADDR` | `redis:6379` | Shared redis for the pacer bucket + rotation counter |
| `SHUTDOWN_TIMEOUT_SECONDS` | `20` | Graceful shutdown budget |
| `DATABASE_URL` | _(empty = webhook platform disabled)_ | Postgres DSN for the `notifications` DB (webhook subscriptions/deliveries) |
| `CONVERSATION_EVENTS_TOPIC` | `opendesk.conversation.events` | Second source topic of the webhook dispatcher |
| `WEBHOOK_GROUP` | `notification-webhooks` | Consumer group of the webhook dispatcher |
| `NOTIFICATIONS_OUTBOX_TOPIC` / `NOTIFICATIONS_OUTBOX_GROUP` | `opendesk.notifications.outbox` / `notification-outbox` | Portal-code command topic + group |
| `WEBHOOK_SIGNING_REQUIRED` | `false` | Reject secret-less webhook subscriptions when `true` (prod) |

## Payments contract

`HoldDeposit` → `POST /activities/hold-deposit` `{booking_id, tenant_id,
amount_cents, currency}` returning `{hold_id}`;
`VoidHold` → `POST /activities/void-hold` `{hold_id, booking_id, tenant_id}`.
The saga threads `hold_id` from the hold response into the compensation; the
hold step is skipped for `price_cents=0` offerings (TigerBeetle transfer codes
per SPEC §9 are applied inside payments-service).

## Tests

`internal/workflows/booking_saga_test.go` uses the Temporal testsuite:
happy-path ordering (with the confirmation send asserted to go through
`NotifyPaced`), reverse-order compensation (`VoidHold` → `ReleaseSlot`)
on `ConfirmBooking` failure, and no compensation when `ReserveSlot` fails.
`waitlist_test.go` / `reminder_test.go` / `industry_packs_test.go` assert
every outbound send (waitlist claim, reminder, deposit reminder, intake
reminder, follow-up, proposal reminder, staff escalation) goes through the
`NotifyPaced` wrapper with order preserved;
`internal/pacer/pacer_test.go` covers burst enforcement, round-robin
rotation (local + redis-INCR) and redis-down fail-open;
`internal/activities/paced_test.go` covers pacing-before-dispatch and
payload validation for every paced kind.

## Run

```bash
go build ./... && go test ./...
./worker
# or
docker build -t opendesk/notification-worker .
```
