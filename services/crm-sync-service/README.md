# crm-sync-service

OpenDesk ⇄ [Twenty CRM](https://twenty.com) **bidirectional** sync (SPEC-CRM
§B): forward event sync (OpenDesk → Twenty) plus a reverse webhook worker
(Twenty → OpenDesk) with echo suppression. Go 1.23, chi router, zap, pgx/v5,
segmentio/kafka-go. Listens on **:7010**, Dapr app-id `crm-sync` (sidecar
`daprd-crm-sync`).

## What it does

1. **Consumes** (consumer group `crm-sync`, direct Kafka via kafka-go):
   | Topic | Event types | Twenty effect |
   |---|---|---|
   | `opendesk.identity.events` | `com.opendesk.identity.TenantProvisioned` | Upsert **Company** `{name, domainName: {primaryLinkUrl: https://<slug>.opendesk.local}}`; `sync_map(kind=tenant)` |
   | `opendesk.booking.events` | `…BookingCreated` / `…BookingConfirmed` | Upsert **Person** by contact email → phone (find-then-create/update via `GET /rest/people?filter=…`), create/patch **Task** `"{offering} appointment at {starts_at}"` linked to the person; `sync_map(kind=contact, kind=booking)` |
   | `opendesk.booking.events` | `…BookingRescheduled` | PATCH task `dueAt` (falls back to the full create path when no mapping exists) |
   | `opendesk.booking.events` | `…BookingCancelled` | PATCH task `status=DONE` + **Note** with the cancellation reason |
   | `opendesk.conversation.events` | `…ToolInvoked` (`tool=book_appointment`, status `accepted`/`ok`) | **Note** "Booked via AI receptionist" on the person — only when `detail` carries `phone`/`email`; voice-agent-runtime's current payload (`{offering_id, starts_at}`) has none, so the note is skipped (acked) by design |
   | `opendesk.conversation.events` | `…SessionEnded` (with `quality` + `confirmed_phone`) | **Note** "📞 AI call summary" on the caller's person (fallback path — see *Sentiment-enriched notes* below); `sync_map(kind=quality_note)` |
   | `opendesk.conversation.quality` | `…CallQualityEnriched` | Same note **with avg sentiment**: patches the fallback note in place, or creates it when the plain event was skipped; `sync_map(kind=quality_note)` |

### Sentiment-enriched call-summary notes (Wave 5 #2, eventual consistency)

The voice runtime does not compute sentiment — per-turn scores live in
conversation-service's `turns` table (app/intel.py). So two events describe
one ended call:

1. `SessionEnded` on `opendesk.conversation.events` (voice-agent-runtime):
   quality signals only, **no sentiment**. This service creates the call
   summary note from it as a **fallback** so the CRM note exists even when
   conversation-service is down or the call had no scored turns.
2. `CallQualityEnriched` on `opendesk.conversation.quality`
   (conversation-service, consumer group `conversation-sentiment`): the same
   quality payload **plus `avg_sentiment`** and `turn_sentiment_count`. This
   is the **preferred** path.

Merge logic (dedupe by conversation id via `sync_map(kind=quality_note)`,
conversation UUID → Twenty note id):

- Enriched arrives after the fallback created the note → the note body is
  **patched** in place to include `avg sentiment ±N.NN` (no duplicate).
- Enriched arrives first (or the plain event was skipped for missing
  quality/phone/person) → the enriched path **creates** the note; a late
  plain `SessionEnded` sees the mapping and does nothing.

Honest limitations: the two consumers run concurrently in one group, so a
create/create race on near-simultaneous delivery can yield one duplicate
note (never lost sentiment); and a call whose turns were never
sentiment-scored keeps the fallback note without sentiment forever.
2. **Serves HTTP**:
   - `GET /healthz` — liveness + Postgres ping
   - `GET /metrics` — Prometheus text format (`crm_sync_counter{name=…}`,
     `crm_sync_latency_seconds_{count,sum,max}{op=…}`,
     `crm_sync_twenty_call_duration_seconds` histogram with
     `method`+`path_class` labels, observed around every Twenty REST call)
   - `POST /webhooks/twenty` — reverse intake; verifies
     `X-Twenty-Webhook-Signature` (HMAC-SHA256 over the raw body, hex or
     base64, optional `sha256=` prefix) against `TWENTY_WEBHOOK_SECRET`, then
     publishes a CloudEvent (`com.opendesk.crm.twenty.<event>`) to
     **`opendesk.crm.events`** via Dapr pubsub `pubsub-kafka`
   - `POST /v1/tasks` — helper for Temporal activities. Accepts BOTH shapes:
     - canonical: `{personId|phone|email, title, body, dueAt}`
     - industry activities (notification-worker `CreateStaffAlertTask` /
       `CreateCRMFollowupTask`): `{tenant_slug, tenant_id, kind
       ("staff_alert"|"follow_up"), title, body, booking_id, due_at}`
     `due_at` (RFC3339) is an alias for `dueAt` (`dueAt` wins if both are
     sent). Person resolution order: `personId` → `booking_id` via
     `sync_map(kind=booking_contact)` → Twenty lookup by email/phone → none.
     `staff_alert` tasks are created **unlinked** by design; `follow_up`
     tries to link but degrades to unlinked (warning log) rather than 4xx.
     Response `201 {taskId, personId, linked}`.
3. **Reverse worker** (consumer group `crm-sync-reverse`, topic
   `opendesk.crm.events`, DLQ after 3 attempts) — Twenty → OpenDesk:
   | CloudEvent type | Action |
   |---|---|
   | `com.opendesk.crm.twenty.person.created` / `…person.updated` | `GET /rest/people/{id}`, resolve tenant slug (contact mapping → company domain `<slug>.opendesk.local`; fallback: person.companyId + `sync_map kind=tenant`), Dapr-invoke booking-service `POST /internal/contacts/upsert` with `external_source='twenty'`, `external_id=<personId>`, `X-Tenant-Slug` header |
   | `com.opendesk.crm.twenty.task.updated` (status `DONE`) | Reverse-lookup booking via `sync_map kind=booking_task`, Dapr-invoke `POST /internal/bookings/{id}/crm-note` |
   Loop prevention: booking-service's reverse write path emits **no** events
   (contacts have no outbox; crm-notes bypass it), and inbound person
   webhooks within `REVERSE_ECHO_WINDOW_SECONDS` (default 10s) of our own
   forward write (`sync_map.last_synced_at`) are suppressed + acked. The
   forward syncer writes `sync_map(kind='booking_task')` alongside
   `kind='booking'` for every task it creates. See
   [docs/integrations/twenty-crm.md](../../docs/integrations/twenty-crm.md)
   for the full reverse design, the remaining 10s-window race and the
   cancel-vs-DONE echo caveat.

## Environment

| Var | Default | Notes |
|---|---|---|
| `PORT` | `7010` | HTTP listen port |
| `DATABASE_URL` | — (required) | Postgres DSN for the `crm_sync` DB (created by `infra/postgres/init-scripts/00-create-dbs.sql`) |
| `KAFKA_BROKERS` | `kafka:9092` | comma-separated broker list |
| `IDENTITY_EVENTS_TOPIC` | `opendesk.identity.events` | |
| `BOOKING_EVENTS_TOPIC` | `opendesk.booking.events` | |
| `CONVERSATION_EVENTS_TOPIC` | `opendesk.conversation.events` | |
| `QUALITY_EVENTS_TOPIC` | `opendesk.conversation.quality` | CallQualityEnriched intake (Wave 5 #2) |
| `CRM_EVENTS_TOPIC` | `opendesk.crm.events` | webhook egress topic (provisioned by `infra/kafka/create-topics.sh`) |
| `CONSUMER_GROUP` | `crm-sync` | shared by the forward readers |
| `REVERSE_CONSUMER_GROUP` | `crm-sync-reverse` | reverse worker group on `CRM_EVENTS_TOPIC` |
| `BOOKING_APP_ID` | `booking` | Dapr app-id for reverse invokes into booking-service |
| `REVERSE_ECHO_WINDOW_SECONDS` | `10` | suppress inbound person webhooks this soon after our own forward write |
| `DLQ_TOPIC` | `opendesk.dlq` | |
| `CONSUMER_ENABLED` | `true` | `false` runs HTTP only (useful for local debugging) |
| `DAPR_HOST` / `DAPR_HTTP_PORT` | `daprd-crm-sync` / `3500` | sidecar address |
| `DAPR_PUBSUB_NAME` | `pubsub-kafka` | pubsub component (scope `crm-sync` is declared) |
| `TWENTY_API_URL` | `http://twenty-api:3000` | |
| `TWENTY_API_KEY` | — (dev placeholder) | see below |
| `TWENTY_WEBHOOK_SECRET` | — | when empty, `/webhooks/twenty` rejects everything |
| `TWENTY_RATE_PER_MIN` | `90` | token-bucket rate for Twenty REST calls |
| `SHUTDOWN_TIMEOUT_SECONDS` | `20` | graceful shutdown budget |

### Creating the Twenty API key (runbook)

1. Start the stack: `docker compose up -d twenty-api twenty-worker twenty-redis`
   (image pinned to `twentycrm/twenty:v1.3.2`; if Docker Hub lacks that tag,
   use `:v1.3.1` — confirmed to exist — or `:latest`, see
   `infra/docker-compose.crm.yml`).
2. Open http://localhost:3100 (or `https://<gateway>/crm/` through APISIX) and
   create the admin account.
3. Go to **Settings → API & Webhooks → API keys → Create key**; copy it.
4. Export it for compose: `TWENTY_API_KEY=<key> docker compose up -d crm-sync`
   (compose default is a non-working dev placeholder).
5. For the reverse direction, register the **webhook** idempotently with
   `infra/twenty/setup-webhooks.sh` (operations `person.created`,
   `person.updated`, `task.updated`, target
   `http://crm-sync:7010/webhooks/twenty`) and set `TWENTY_WEBHOOK_SECRET`
   to the same shared secret. Manual alternative: create the webhook in the
   same settings page.

## sync_map

Bootstrapped idempotently on startup (schema per SPEC-CRM §B):

```
sync_map(id serial, kind text, opendesk_id text, twenty_id text,
         tenant_id uuid, updated_at timestamptz, last_synced_at timestamptz,
         UNIQUE(kind, opendesk_id, tenant_id))
-- plus: index on (kind, twenty_id) for reverse lookups
```

Kinds: `tenant` (tenant UUID → Twenty company id), `contact` (booking contact
UUID → Twenty person id), `booking` (booking UUID → Twenty task id),
`booking_task` (booking UUID → Twenty task id; reverse lookup for
`task.updated` DONE webhooks), `booking_contact` (booking UUID → Twenty
person id; used by `/v1/tasks` to
resolve "the person of booking X"), `quality_note` (conversation UUID →
Twenty note id; dedupe between the SessionEnded fallback and the
CallQualityEnriched note paths, Wave 5 #2). A nil
tenant is stored as the zero UUID so the UNIQUE constraint dedupes correctly
(Postgres treats NULLs as distinct). `last_synced_at` is stamped by every
forward-sync `Put` and feeds the reverse worker's echo suppression.

Inspect: `docker compose exec postgres psql -U opendesk -d crm_sync -c 'select * from sync_map order by updated_at desc limit 20;'`

## Retries, rate limiting, DLQ

- Twenty calls: token bucket (`TWENTY_RATE_PER_MIN`, default 90/min); retries
  with exponential backoff on 429/5xx (up to 4 attempts). 4xx ≠ 429 fails
  fast. Batch awareness: Twenty `/rest/batch/*` accepts ≤60 ops/call — this
  client uses single-record endpoints only; chunk to ≤60 if batching is added.
- Kafka processing: each message is attempted **3 times** (500ms · attempt
  backoff). Poison payloads (unparseable CloudEvent, missing required fields)
  skip retries. Failures land in **`opendesk.dlq`** as
  `{source_topic, event_id, event_type, error, failed_at, payload}`; the
  original offset is committed after the DLQ write so the pipeline never
  stalls. Replaying = republishing `payload` to `source_topic`.
- Delivery is at-least-once end to end; all Twenty writes are upserts keyed by
  `sync_map`, so redelivery is safe.

## Development

```
go build ./... && go vet ./... && go test ./...
CONSUMER_ENABLED=false DATABASE_URL=postgres://opendesk:opendesk@localhost:5432/crm_sync \
  TWENTY_API_URL=http://localhost:3100 TWENTY_API_KEY=... go run ./cmd/server
```
