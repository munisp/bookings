# conversation-service

Conversation session + turn persistence and transcript streaming for OpenDesk
(SPEC §4 topics, §7 conversation schema, §11 voice pipeline). Python 3.12,
FastAPI, asyncpg, structlog.

## Endpoints

| Method | Path | Description |
|---|---|---|
| GET | `/healthz` | Liveness (pings Postgres) |
| POST | `/v1/conversations` | Start a conversation `{tenant_id, site_slug, channel}` (channel: voice\|chat\|phone\|api) |
| POST | `/v1/conversations/{id}/turns` | Append a turn `{role, text, tool_calls?, audio_url?}` (role: user\|agent\|system\|tool); seq assigned atomically |
| GET | `/v1/conversations?tenant=<uuid>` | List tenant conversations (paged) |
| GET | `/v1/conversations/{id}?tenant=<uuid>` | Conversation with all turns |

**Tenant scope** is required on every request via `?tenant=<uuid>` or the
`X-Tenant-ID` header. The schema enforces `FORCE ROW LEVEL SECURITY`
(`app.tenant_id` setting), so the service sets it per transaction — requests
without tenant scope are rejected `400`; cross-tenant rows are invisible.

## Turn pipeline (per accepted turn)

1. Insert into Postgres (`turns`, next `seq`, in a transaction with an
   advisory lock per conversation).
2. Publish the **raw record** `{conversationId, tenantId, role, text, ts}` to
   topic `opendesk.transcripts-raw` through a `TranscriptSink` (SPEC §5):
   - `TRANSCRIPT_SINK=fluvio` → `FluvioSink` (official fluvio python client,
     import guarded, sync client wrapped in `asyncio.to_thread`)
   - `TRANSCRIPT_SINK=kafka` (default) → `KafkaSink` (aiokafka producer,
     same topic name on Kafka) — also the automatic fallback when the
     optional `fluvio` package is missing.
3. Always publish a **CloudEvent** (`ConversationTurn` per SPEC §4) to Kafka
   topic `opendesk.conversation.transcripts` via the Dapr HTTP pubsub
   component `pubsub-kafka` (`application/cloudevents+json`).

Postgres is the source of truth: streaming/publish failures are logged and do
not fail the API request.

## Indexer

Background task (aiokafka consumer, group `conversation-service-indexer`)
reading `opendesk.conversation.transcripts` and bulk-indexing documents into
the OpenSearch index `conversations`, conforming to the mapping in
`infra/opensearch/setup-indices.sh` (`tenant_id, conversation_id, site_slug,
channel, role, text, audio_url, ts`). `site_slug`/`channel` are enriched from
Postgres; `redacted` is set by the index's `pii-safe` default ingest pipeline.
Batched flushes (size/time), explicit offset commits after each flush.

## Environment variables

| Var | Default | Description |
|---|---|---|
| `PORT` | `7007` | HTTP listen port |
| `PG_DSN` | `postgres://opendesk:opendesk@postgres:5432` | Base Postgres DSN |
| `PG_DATABASE` | `conversation` | Database name (appended to PG_DSN) |
| `PG_MIN_SIZE` / `PG_MAX_SIZE` | `1` / `10` | Pool sizes |
| `DAPR_HOST` / `DAPR_HTTP_PORT` | `daprd-conversation` / `3500` | daprd sidecar |
| `DAPR_PUBSUB_NAME` | `pubsub-kafka` | Dapr pubsub component |
| `TRANSCRIPTS_TOPIC` | `opendesk.conversation.transcripts` | CloudEvent topic |
| `TRANSCRIPT_SINK` | `kafka` | Raw transcript sink: `fluvio` or `kafka` |
| `FLUVIO_TOPIC` | `opendesk.transcripts-raw` | Raw transcript topic |
| `KAFKA_BROKERS` | `kafka:9092` | Broker list (sink + indexer) |
| `OPENSEARCH_ADDR` | `http://opensearch:9200` | OpenSearch address |
| `CONVERSATIONS_INDEX` | `conversations` | Target index |
| `INDEXER_ENABLED` | `true` | Run the indexer task |
| `INDEXER_GROUP` | `conversation-service-indexer` | Consumer group |
| `INDEXER_BULK_SIZE` / `INDEXER_BULK_FLUSH_SECONDS` | `100` / `2` | Bulk batching |

## Run

```bash
python -m venv .venv && . .venv/bin/activate
pip install -e .            # add [fluvio] for the Fluvio sink
python -m app.main
# or
docker build -t opendesk/conversation-service .
```

## Notes

- The fluvio dependency is optional (`pip install -e .[fluvio]`); the import
  is guarded so the image runs without it (default `kafka` sink).
- The Dockerfile builds with `pip install .` (core deps only); install the
  `fluvio` extra in a custom image when `TRANSCRIPT_SINK=fluvio`.

## GDPR contact marker + privacy erasure (SPEC-W3 §2, innovation 13)

- `conversations.contact_phone` (nullable, idempotent
  `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` at startup + index) marks the
  data subject behind a conversation. It is populated at creation time from
  the caller's site/session metadata (`POST /v1/conversations` with
  `contact_phone`) when the visitor's phone (or e-mail) is known — anonymous
  sessions stay NULL. It is deliberately a single flat marker column instead
  of scanning turn text (PII minimization + cheap indexed lookups).
- `GET /v1/conversations?tenant=<uuid>&contact=<phone|email>` filters on
  this marker (used by `GdprExportWorkflow`).
- The privacy erase consumer (`app/privacy.py`, direct aiokafka like the
  indexer) consumes `PrivacyEraseRequested` tombstones from
  `opendesk.privacy.events` and deletes all turns of matching conversations,
  then clears the marker; conversation shells are kept for referential
  integrity. Env: `PRIVACY_ENABLED` (default true), `PRIVACY_EVENTS_TOPIC`,
  `PRIVACY_EVENTS_GROUP`.
