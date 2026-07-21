# Twenty CRM Integration

OpenDesk syncs tenants, contacts and bookings one-way into a self-hosted
[Twenty](https://twenty.com) CRM, and accepts a minimal reverse webhook intake
from Twenty back into the platform. This guide covers the architecture, setup,
event mapping, rate limits and day-2 operations.

## Architecture

```
                        ┌────────────────────────── OpenDesk compose ──────────────────────────┐
                        │                                                                      │
 identity-service ──┐   │                                                                      │
 booking-service  ──┼──►│  Kafka topics                                                        │
 conversation-svc ──┘   │   opendesk.identity.events ─┐                                        │
                        │   opendesk.booking.events ──┼──► crm-sync (:7010, daprd-crm-sync)    │
                        │   opendesk.conversation.events ┘   │  consumer group "crm-sync"       │
                        │                                    │  token bucket 90 req/min        │
                        │   opendesk.dlq ◄── poison messages─┘  (3 attempts, then DLQ)         │
                        │                                                                      │
                        │   crm-sync ── REST (Bearer API key) ──► twenty-api (:3000)           │
                        │        │                                  │     ▲                    │
                        │        │ sync_map (crm_sync DB)           │     │ Bull-MQ jobs       │
                        │        ▼                                  ▼     │                    │
                        │     postgres                        twenty-worker ──► twenty-redis   │
                        │   (DBs: twenty, crm_sync)                                                │
                        │                                                                      │
                        │   Twenty webhook ──► POST :7010/webhooks/twenty (HMAC) ──► Kafka      │
                        │                                          opendesk.crm.events         │
                        └──────────────────────────────────────────────────────────────────────┘

 Browser:  CRM UI  http://localhost:3100        (linked from admin-web sidebar)
 Gateway:  APISIX  /crm/*  ──proxy-rewrite──►  twenty-api:3000   (openid-connect bearer_only)
```

### Compose services (root `docker-compose.yml`)

| Service | Image / build | Ports | Purpose |
|---|---|---|---|
| `twenty-api` | `twentycrm/twenty:v1.3.2` | `3100:3000` | Twenty server (REST + frontend) |
| `twenty-worker` | `twentycrm/twenty:v1.3.2` (`yarn worker:prod`) | — | Background job worker (Bull-MQ) |
| `twenty-redis` | `redis:7-alpine` | internal | Queue + cache for Twenty |
| `crm-sync` | `services/crm-sync-service` (Go 1.23) | `7010` | OpenDesk ⇄ Twenty bridge |
| `daprd-crm-sync` | daprd sidecar (`DAPR_APP_ID=crm-sync`) | internal | Pub/sub + service invocation |

Twenty stores its data in the shared `postgres` container (database
`twenty`, created by `infra/postgres/init-scripts/00-create-dbs.sql`); crm-sync
keeps its own `crm_sync` database for the `sync_map` table. Twenty env:
`PG_DATABASE_URL=postgres://opendesk:opendesk@postgres:5432/twenty`,
`REDIS_URL=redis://twenty-redis:6379`, `SERVER_URL`/`FRONT_BASE_URL=
http://localhost:3100`, `MESSAGE_QUEUE_TYPE=bull-mq`, `STORAGE_TYPE=local`,
plus token secrets with `-dev` fallbacks
(`ACCESS_TOKEN_SECRET`, `LOGIN_TOKEN_SECRET`, `REFRESH_TOKEN_SECRET`,
`FILE_TOKEN_SECRET`, `APP_SECRET`).

## Creating a Twenty API key

1. Open **http://localhost:3100** and complete the workspace setup (first user
   becomes the workspace admin).
2. Go to **Settings → API & Webhooks** (⚙ → Developers in some versions).
3. **Create API key** — give it a name (`opendesk-crm-sync`) and copy the
   token; it is shown once.
4. Set it on the sync service in the root compose file (or `.env`):

   ```yaml
   crm-sync:
     environment:
       TWENTY_API_KEY: eyJhbGciOi...   # replace the placeholder dev value
   ```

5. `docker compose up -d crm-sync` to pick up the new key.

All Twenty calls use `Authorization: Bearer <key>` against
`TWENTY_API_URL` (`http://twenty-api:3000` in-compose).

## Sync mapping (OpenDesk → Twenty)

| Kafka topic | CloudEvent type | Twenty action | sync_map rows |
|---|---|---|---|
| `opendesk.identity.events` | `TenantProvisioned` | Upsert **Company** `{name, domainName: "<slug>.opendesk.local"}` | `kind=tenant` |
| `opendesk.booking.events` | `BookingCreated` / `BookingConfirmed` | `findPerson` by contact email/phone (`GET /rest/people?filter=emails.primaryEmail[eq]...`), then POST or PATCH **Person**; create **Task** `"{offering} appointment at {starts_at}"` linked to the person | `kind=contact`, `kind=booking` |
| `opendesk.booking.events` | `BookingCancelled` | PATCH the Task — status done + cancellation note | `kind=booking` (updated) |
| `opendesk.booking.events` | `BookingRescheduled` | PATCH the Task `dueDate` | `kind=booking` (updated) |
| `opendesk.conversation.events` | `ToolInvoked(book_appointment)` | Optional **Note** on the person: “Booked via AI receptionist” | — |

Upserts are keyed by `sync_map`
`UNIQUE(kind, opendesk_id, tenant_id)`, so re-delivered events are idempotent.
Events for unknown tenants/companies are retried (transient ordering) and
dead-lettered after 3 attempts — see Troubleshooting.

## Reverse intake (Twenty → OpenDesk)

Twenty webhooks (Settings → API & Webhooks → Webhooks) can point at:

```
POST http://localhost:7010/webhooks/twenty
```

crm-sync verifies the `X-Twenty-Webhook-Signature` HMAC header against
`TWENTY_WEBHOOK_SECRET`, logs the payload, and emits a CloudEvent to
`opendesk.crm.events` (topic created by `infra/kafka/create-topics.sh`).
Consumers downstream (e.g. the support-desk escalation workflow) can subscribe
via Dapr pub/sub — the `crm-sync` app-id is already in the pubsub component
scopes.

Requests with a missing/invalid signature get `401` and are **not** retried by
the sender unless the webhook is re-enabled — check
`docker compose logs crm-sync` for `invalid webhook signature` entries.

## Rate limits & batching

Twenty Cloud defaults are generous, but the self-hosted API is guarded by the
sync client regardless:

* **Token bucket, 90 requests/min** (`TWENTY_RATE_PER_MIN=90`) — all outbound
  REST calls pass through the limiter.
* **Batch endpoints: max 60 objects per call** — when bulk-creating
  people/tasks, split payloads into batches of ≤ 60.
* **Retries:** exponential backoff with jitter on `429` and `5xx`; other 4xx
  responses are treated as poison and go to the DLQ after the attempt budget.

Metrics are on `:7010/metrics`: per-event-type counters
(`crm_sync_events_total{type=...}`) and Twenty call latency
(`crm_sync_twenty_call_duration_seconds`).

## sync_map & DLQ operations

```bash
# What is mapped where:
docker exec postgres psql -U opendesk -d crm_sync -c \
  "SELECT kind, opendesk_id, twenty_id, tenant_id, updated_at FROM sync_map
   ORDER BY updated_at DESC LIMIT 20;"

# Unmapped tenants (should be empty in steady state):
docker exec postgres psql -U opendesk -d identity -c "SELECT id, slug FROM tenants;" \
  # vs sync_map rows with kind='tenant'
```

Full DLQ replay procedure for crm-sync (including the rekey procedure and
Twenty upgrades) lives in [docs/runbooks/operations.md §7](../runbooks/operations.md).

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `401` from Twenty in crm-sync logs | Placeholder/expired `TWENTY_API_KEY` | Create a key (above), set env, restart `crm-sync` |
| Companies created, people/tasks not | `BookingCreated` arrived before `TenantProvisioned` was processed | Transient — consumer retries; check `opendesk.dlq` only if it persists |
| Events in `opendesk.dlq` with `404` from Twenty | Object deleted in Twenty, or bad payload | Inspect DLQ message, fix root cause, replay to original topic (runbook §7) |
| `429` bursts, rising call latency | Rate limiter too tight or a retry loop | Verify `TWENTY_RATE_PER_MIN`; check `/metrics` latency histogram |
| Webhook `401 invalid signature` | `TWENTY_WEBHOOK_SECRET` ≠ secret configured in Twenty | Re-copy the webhook secret; both sides must match byte-for-byte |
| Twenty UI up but API unreachable in-compose | `twenty-worker` down (migrations run there) | `docker compose logs twenty-worker`; worker runs DB migrations on boot |
| `domainName` conflicts on Company upsert | Two tenants sharing a slug after a rekey | Re-map via sync_map (runbook §7 rekey procedure) |
| Nothing syncing at all | crm-sync or sidecar down / Kafka lag | `docker compose ps crm-sync daprd-crm-sync`; `kafka-consumer-groups.sh --describe --group crm-sync` |
