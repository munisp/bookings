# analytics-pipeline (port 7009)

Kafka → Iceberg **bronze** sink for the OpenDesk lakehouse (SPEC §4 topics, §13 stack),
plus a FastAPI sidecar exposing health and Prometheus metrics.

```
Kafka topics (CloudEvents 1.0)                     Iceberg REST catalog :8181
  opendesk.booking.events  ─┐   micro-batch          warehouse s3://lake/warehouse (MinIO)
  opendesk.payments.events ─┼─ (BATCH_SIZE / ──▶  bronze.booking_events
  opendesk.conversation.   ─┘   FLUSH_INTERVAL)   bronze.payment_events
    transcripts                                   bronze.transcripts
```

## How it works

1. One `aiokafka` consumer (group `analytics-pipeline`, `enable_auto_commit=False`)
   subscribes to the three topics and buffers messages **per topic**.
2. A buffer flushes when it reaches `BATCH_SIZE` messages **or** ages past
   `FLUSH_INTERVAL` seconds (checked every 1 s).
3. Flush = map payloads to bronze rows → `pyarrow.Table` → `pyiceberg` `table.append()`
   against the REST catalog. Offsets are committed **only after** a successful append:
   delivery is **at-least-once**; duplicates are removed downstream by the Spark silver
   jobs (`infra/lakehouse/spark/jobs`, dedupe on `event_id` / `(conversation_id, ts, role)`).
4. On startup the service retries until Kafka and the REST catalog are reachable, and
   auto-creates namespace `bronze` + the three tables with explicit pyiceberg schemas
   (`AUTO_CREATE_TABLES=true`).

## Bronze schema contract (consumed by dbt — do not drift)

Column names/order match `infra/lakehouse/dbt/models/silver/schema.yml` sources and are
guarded by `tests/test_dbt_conformance.py`:

| Table | Columns (Iceberg types) |
|---|---|
| `bronze.booking_events` | `event_id` string, `event_type` string, `tenant_id` string, `booking_id` string, `status` string, `source` string, `price_cents` long, `currency` string, `starts_at` timestamp, `occurred_at` timestamp, `offering_id` string *(appended in SPEC-W3 §3 for revenue intelligence; existing tables get it via Iceberg schema evolution in `ensure_bronze`, old rows read NULL)* |
| `bronze.payment_events` | `event_id` string, `event_type` string, `tenant_id` string, `booking_id` string, `amount_cents` long, `currency` string, `transfer_code` long, `ledger_ref` string, `occurred_at` timestamp |
| `bronze.transcripts` | `conversation_id` string, `tenant_id` string, `role` string, `text` string, `ts` timestamp, `audio_url` string |

Mapping rules (`analytics_pipeline/mapping.py`):

- **CloudEvents envelope**: `event_id ← id`, `occurred_at ← time`, `tenant_id ← tenantid`
  extension (falls back to `data.tenantId`), `booking_id ← data.bookingId` (falls back to
  `subject`). `event_type` keeps only the **last segment** of `type`
  (`com.opendesk.booking.BookingCreated` → `BookingCreated`) so dbt's `lower(event_type)`
  comparisons hold.
- **Payload keys** are read in camelCase *or* snake_case (`bookingId`/`booking_id`, …).
- **Transcripts** also accept bare `ConversationTurn` messages (no envelope) for the raw
  Fluvio-fed path.
- **Timestamps** are naive UTC (Iceberg `timestamp` without timezone), consistent with the
  Spark jobs' `spark.sql.iceberg.handle-timestamp-without-timezone=true`. ISO-8601,
  epoch seconds and epoch millis inputs are accepted.

## How dbt gold marts consume bronze

`infra/lakehouse/dbt` reads these tables through Trino (`iceberg.bronze.*`):
`silver/stg_booking_events` + `stg_transcripts` standardize casing/enums as **views**;
gold tables aggregate them — `daily_bookings_per_tenant`, `revenue_daily` (uses
`transfer_code` 101/102/103), `no_show_rate`, `agent_containment_rate` (a conversation is
contained when it has zero `role = 'human_agent'` turns). The Spark silver jobs also read
bronze directly to produce deduplicated `silver.*` Iceberg tables. Because dbt tests
assert `accepted_values` on lowercased `event_type`/`source`/`role`, the sink preserves
raw casing and lets dbt normalize.

## Sidecar API (port 7009)

| Endpoint | Description |
|---|---|
| `GET /healthz` | 200 once Kafka consumer is running and Iceberg bootstrap done (503 while starting). Body: per-topic `lag` (highwater − position), buffered count, target table, `last_error`. |
| `GET /metrics` | Prometheus text: `analytics_messages_consumed_total`, `analytics_rows_written_total`, `analytics_flushes_total{outcome}`, `analytics_flush_duration_seconds`, `analytics_buffer_messages`, `analytics_consumer_lag`, `analytics_consumer_running`. |
| `GET /v1/recommendations?tenant=<uuid>` | SPEC-W3 §3 innovation 9: latest pricing recommendation per offering for the tenant, read via pyiceberg scan of `gold.reco_pricing` (written by `infra/lakehouse/spark/jobs/revenue_intelligence.py`; uses the same `ICEBERG_REST_URI`/S3 env as the sink). Returns `{"tenant", "recommendations": [...]}` with peak-hour stats, `suggested_peak_multiplier` and `suggested_deposit_pct`; **empty list when the table does not exist yet** (no Spark run), 502 on lakehouse errors. |

## Environment variables

| Var | Default | Purpose |
|---|---|---|
| `KAFKA_BOOTSTRAP_SERVERS` | `kafka:9092` | Kafka brokers (core compose). |
| `KAFKA_GROUP_ID` | `analytics-pipeline` | Consumer group. |
| `TOPIC_BOOKING_EVENTS` | `opendesk.booking.events` | Source topic → `bronze.booking_events`. |
| `TOPIC_PAYMENT_EVENTS` | `opendesk.payments.events` | Source topic → `bronze.payment_events`. |
| `TOPIC_TRANSCRIPTS` | `opendesk.conversation.transcripts` | Source topic → `bronze.transcripts`. |
| `BATCH_SIZE` | `500` | Flush threshold per topic buffer. |
| `FLUSH_INTERVAL` | `15` (seconds) | Max buffer age before flush. |
| `ICEBERG_REST_URI` | `http://iceberg-rest:8181` | Iceberg REST catalog. |
| `ICEBERG_WAREHOUSE` | `s3://lake/warehouse` | Warehouse root (bucket `lake`). |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | `minioadmin` / `minioadmin` | MinIO credentials. |
| `AWS_ENDPOINT_URL` | `http://minio:9000` | S3 endpoint (mapped to `s3.endpoint`). |
| `AWS_REGION` | `us-east-1` | S3 region. |
| `AUTO_CREATE_TABLES` | `true` | Create `bronze` namespace + tables on boot. |
| `STARTUP_RETRY_SECONDS` / `STARTUP_MAX_ATTEMPTS` | `5` / `60` | Dependency wait loop. |
| `PORT` / `HOST` | `7009` / `0.0.0.0` | Sidecar bind. |

## Run

```bash
# local (needs Kafka + lakehouse compose tiers up, reachable hostnames)
pip install .[dev]
python -m analytics_pipeline.main

# tests (pure stdlib fallbacks included; no pytest required)
python tests/test_mapping.py && python tests/test_dbt_conformance.py

# docker
docker build -t opendesk/analytics-pipeline .
docker run --rm --network opendesk -p 7009:7009 opendesk/analytics-pipeline
```

Verify data lands:

```sql
-- via Trino on :8088
SELECT * FROM iceberg.bronze.booking_events LIMIT 5;
```
