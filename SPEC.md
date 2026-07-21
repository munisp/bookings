# SPEC.md — OpenDesk: SOTA Open-Source AI Receptionist Platform

Single source of truth. All subagents implement to these contracts exactly.

## 1. Overview
OpenDesk is a fully open-source, multi-tenant AI receptionist / front-desk SaaS that supersedes the YouTube demo "Switchboard" (Next.js + Convex + Clerk + ElevenLabs). Every proprietary dependency is replaced by SOTA open-source equivalents, and the platform gains an enterprise middleware backbone.

### Baseline parity features (must exist)
- Multi-tenant orgs; tenant dashboard; branded public booking page `/p/{siteSlug}`
- Voice + text AI concierge with 6 tools: `get_business_info`, `get_availability`, `book_appointment`, `lookup_appointment`, `reschedule_appointment`, `cancel_appointment`
- Catalog (offerings w/ duration/buffer/price/capacity), team members + weekly availability, contacts, bookings, knowledge base, per-tenant terminology/timezone/currency/locale
- Phone-number confirmation policy before mutations; tenant-safe tool resolution (server resolves org from slug, never from model)

### SOTA upgrades over baseline
- Open-source voice stack (LiveKit Agents + faster-whisper STT + Piper TTS + Ollama/vLLM LLM) with optional ElevenLabs adapter
- Event-driven backbone (Kafka + Fluvio), durable workflows (Temporal sagas), ReBAC authz (Permify), OIDC authn (Keycloak), payments ledger (TigerBeetle) + Mojaloop rails, hybrid search/RAG (OpenSearch), analytics lakehouse (Iceberg+MinIO+Trino+Spark+dbt), WAF (OpenAppSec) behind APISIX gateway

## 2. Monorepo layout (repo root = /mnt/agents/output/opendesk)
```
infra/
  docker-compose.yml            # single root compose; profiles: core, voice, lakehouse
  apisix/                       # config.yaml, apisix.yaml routes, openappsec plugin cfg
  openappsec/                   # agent policy files
  keycloak/                     # realm-opendesk.json import
  permify/                      # schema.perm, startup cfg
  dapr/components/              # pubsub.kafka.yaml, pubsub.fluvio.yaml, statestore.redis.yaml,
                                #   secretstores.yaml, bindings.smtp.yaml, bindings.twilio.yaml, config.yaml
  temporal/                     # dynamicconfig, entrypoint
  postgres/                     # init-scripts/00..n.sql (one DB per service)
  kafka/                        # topics-init job config
  fluvio/                       # smartmodule (PII redaction) README + topic setup script
  opensearch/                   # index templates, ingest pipelines, opensearch_dashboards cfg
  mojaloop/                     # mojaloop-simulator / mini-loop cfg
  lakehouse/                    # minio init, iceberg rest catalog cfg, trino catalog props, spark jobs dir, dbt project
services/
  identity-service/             # Go 1.23, chi router
  booking-service/              # Go 1.23, chi router
  notification-worker/          # Go 1.23, Temporal worker
  payments-service/             # Rust (axum), TigerBeetle + Mojaloop adapter
  gateway-edge/                 # Rust (axum + tokio), WebSocket fan-out, Fluvio consumer
  voice-agent-runtime/          # Python 3.12, LiveKit Agents
  conversation-service/         # Python 3.12, FastAPI
  knowledge-service/            # Python 3.12, FastAPI
  analytics-pipeline/           # Python 3.12, Kafka→Iceberg sink + Spark jobs + dbt
apps/
  admin-web/                    # Next.js 15 (App Router, TS, Tailwind), dashboard + public page
docs/
  ARCHITECTURE.md  ADRs/  api/openapi/*.yaml  runbooks/
scripts/
  make targets helper scripts, smoke-test.sh
Makefile
README.md
```

## 3. Network & ports (docker network `opendesk`)
| Service | Container | Ports (host) |
|---|---|---|
| APISIX gateway | apisix | 9080 (proxy), 9180 (admin) |
| etcd (apisix) | apisix-etcd | internal |
| OpenAppSec nano-agent | openappsec | attached to apisix container |
| Keycloak | keycloak | 8080 |
| Permify | permify | 3476 (http), 3478 (grpc) |
| Kafka (KRaft, bitnami) | kafka | 9092 |
| Fluvio (sc + spu) | fluvio | 9003 |
| Temporal server+ui | temporal, temporal-ui | 7233, 8233 |
| Postgres 16 | postgres | 5432 |
| Redis 7 | redis | 6379 |
| TigerBeetle | tigerbeetle | 3000 |
| Mojaloop simulator (moja-sim) | mojaloop | 8444 |
| OpenSearch + dashboards | opensearch, opensearch-dashboards | 9200, 5601 |
| MinIO | minio | 9000, 9001 |
| Iceberg REST catalog | iceberg-rest | 8181 |
| Trino | trino | 8088 |
| Spark master/worker | spark-* | 7077, 8081 |
| identity-service | identity | 7001 |
| booking-service | booking | 7002 |
| notification-worker | notification | 7003 |
| payments-service | payments | 7004 |
| gateway-edge | edge | 7005 |
| voice-agent-runtime | voice | 7006 (+ LiveKit 7880/7881) |
| conversation-service | conversation | 7007 |
| knowledge-service | knowledge | 7008 |
| analytics-pipeline | analytics | 7009 |
| admin-web | web | 3001 |

Dapr sidecar http ports: app port +1000 (8001..8009). Every service runs with a Dapr sidecar (`dapr.io/enabled` via compose `daprd` container sharing network namespace is NOT used; instead each app service has a companion `daprd-<svc>` container on the same compose profile, app talks to `daprd-<svc>:3500`).

## 4. Kafka topics (all `-partitions 6 -rf 1` in dev)
- `opendesk.booking.commands` — key: bookingId; events: BookAppointment, RescheduleAppointment, CancelAppointment
- `opendesk.booking.events` — BookingCreated/Confirmed/Rescheduled/Cancelled/NoShow (CloudEvents JSON)
- `opendesk.conversation.transcripts` — ConversationTurn {conversationId, tenantId, role, text, ts, audioUrl?}
- `opendesk.conversation.events` — SessionStarted/Ended, ToolInvoked
- `opendesk.payments.commands` / `opendesk.payments.events` — ChargeDeposit, Refund, NoShowFee; PaymentPosted(ledgerRef)
- `opendesk.identity.events` — TenantProvisioned, MemberInvited, RoleChanged
- `opendesk.notifications.outbox` — SendReminder, SendConfirmation
- `opendesk.dlq` — dead letters
CloudEvents 1.0 envelope everywhere: {specversion, id, source, type, subject, time, tenantid (ext), data}.

## 5. Fluvio
- Topic `opendesk.transcripts-raw` fed by conversation-service for high-throughput edge/telephony ingestion.
- WASM smart module `pii-redact` (Rust, fluvio-smartmodule) filters/redacts phone/emails before sink to OpenSearch + lakehouse. Provide smart module source under `infra/fluvio/pii-redact/`.
- gateway-edge consumes `opendesk.booking.events` via Fluvio mirror + Kafka fallback for WS fan-out.

## 6. Temporal workflows (namespace `opendesk`)
- `BookingSagaWorkflow` — activities: ReserveSlot (booking-svc), HoldDeposit (payments-svc), ConfirmBooking, SendConfirmation (notification); compensations: ReleaseSlot, VoidHold. SAGA with explicit compensation order.
- `ReminderWorkflow` — timers at T-24h/T-1h, cancel on booking event signal.
- `NoShowFollowupWorkflow`, `TenantOnboardingWorkflow` (Keycloak group, Permify schema/tenant, Postgres seed, OS index alias).
Task queue: `opendesk-main`. Go worker in notification-worker hosts Go activities; booking/payments activities called via Dapr service invocation HTTP.

## 7. Postgres schemas (init scripts)
DBs: `identity`, `booking`, `conversation`, `knowledge`, `analytics_meta`. Every table has `tenant_id UUID NOT NULL`; enable RLS with policy `tenant_id = current_setting('app.tenant_id')::uuid`.
- booking: `offerings(id,tenant_id,name,description,duration_min,buffer_min,price_cents,currency,capacity,bookable,created_at)`, `team_members(id,tenant_id,name,email,role,active)`, `availability_rules(id,tenant_id,team_member_id,weekday,start_min,end_min,effective_from,effective_to)`, `contacts(id,tenant_id,name,phone,email,notes)`, `bookings(id,tenant_id,offering_id,team_member_id,contact_id,starts_at,ends_at,status,source,idempotency_key UNIQUE,created_at,updated_at)`, `outbox(id,aggregate_id,topic,payload jsonb,sent_at)`
- identity: `tenants(id,slug,name,timezone,currency,locale,terminology jsonb,plan,created_at)`, `memberships(tenant_id,user_id,role)`
- conversation: `conversations(id,tenant_id,site_slug,channel,started_at,ended_at)`, `turns(id,conversation_id,seq,role,text,tool_calls jsonb,ts)`
- knowledge: `documents(id,tenant_id,title,body,source_url,created_at)`, `chunks(id,document_id,seq,content)`

## 8. AuthN/AuthZ
- Keycloak realm `opendesk` (import file): clients `admin-web` (public, PKCE), `service-accounts` (confidential per-service), roles `owner|admin|staff|viewer`; groups map to tenants: `/tenants/{slug}`. Token claim `tenant_slugs` via group membership attribute mapper.
- APISIX `openid-connect` plugin for browser routes; `jwt-auth` for service-to-service; OpenAppSec attachment on gateway.
- Permify schema (`infra/permify/schema.perm`): entities `organization`, `offering`, `booking`, `site`, `user`; relations owner/admin/member/viewer; permissions: `manage_catalog = owner or admin`, `manage_bookings = owner or admin or member`, `view_dashboard = ...`, `publish_site = owner or admin`. booking-service checks Permify gRPC `Check` before mutations (subject user from JWT `sub`, resource `organization:{tenant}`).

## 9. Payments
- TigerBeetle: cluster 0, replica 3000. Accounts per tenant: `tenant:{id}:deposits`, `tenant:{id}:revenue`, platform fee account. Transfers: code 100 deposit hold (pending), 101 capture, 102 refund, 103 no-show fee, 104 payout. payments-service uses official `tigerbeetle-node`? No — Rust: use `tigerbeetle-unofficial`? Constraint: write a minimal TigerBeetle wire client is too much — instead depend on community crate `tigerbeetle` if unavailable, implement thin client over the TigerBeetle **vsr protocol is binary** → pragmatics: use `reqwest`-free approach is wrong. DECISION: payments-service implements the client against TigerBeetle via the official Go client is not available in Rust; use the `tb_client` binary protocol is out of scope → payments-service exposes REST and internally uses a `LedgerClient` trait with two impls: (a) `TigerBeetleClient` using the official `tigerbeetle` crate if it compiles, otherwise (b) `HttpSimClient` hitting a tiny embedded in-process ledger for dev, selected by env `LEDGER_IMPL=tigerbeetle|sim`. Document this in ADR-0007. (This keeps the build green while preserving the TigerBeetle integration contract.)
- Mojaloop: adapter module implementing FSPIOP-style flows `POST /quotes`, `POST /transfers` against mojaloop-simulator for cross-border payout of tenant earnings. Env `MOJALOOP_ENDPOINT=http://mojaloop:8444`.
- Outbox pattern: payments writes to Kafka `opendesk.payments.events` via Dapr pubsub.

## 10. Search & RAG
- OpenSearch index `kb-chunks` (knn_vector 384-dim, embedding from sentence-transformers `all-MiniLM-L6-v2`), `conversations` (transcripts, PII-redacted via Fluvio), `bookings-analytics` (from lakehouse sync).
- knowledge-service: ingest → chunk → embed → bulk index; `/search` hybrid (BM25 + k-NN, RRF). voice runtime calls it for grounding.

## 11. Voice agent runtime (Python, LiveKit Agents)
- Pipeline: VAD (silero) → faster-whisper STT → LLM (Ollama `llama3.1:8b` default; OpenAI-compatible endpoint so vLLM/ElevenLabs adapter pluggable) → Piper TTS.
- Tool definitions mirror the 6 baseline tools; tools execute via Dapr service invocation to booking-service/knowledge-service. Tenant context injected at session start from identity-service (terminology, hours, offerings snapshot). Phone-number confirmation policy enforced in the agent prompt + server-side guard in booking commands (command rejected without verified contact phone).
- LiveKit server included in compose (dev keys) on 7880.

## 12. APISIX routes (apisix.yaml, standalone etcd-less? use etcd)
- `/api/identity/*` → identity:7001, `/api/bookings/*` → booking:7002, `/api/payments/*` → payments:7004, `/api/knowledge/*` → knowledge:7008, `/api/conversations/*` → conversation:7007, `/ws/*` → edge:7005 (websocket), `/voice/*` → voice:7006, `/*` → web:3001.
- Plugins: openid-connect (browser), jwt-auth (services), limit-count (Redis), prometheus, cors, opentelemetry, openappsec (per OpenAppSec docs: attach via apisix plugin `open-appsec` / nginx module — represent as plugin config + ADR).

## 13. Lakehouse
- MinIO bucket `lake`; Iceberg REST catalog (tabulario/iceberg-rest fixed 0.6 image or apache/iceberg-rest-fixture) backed by Postgres catalog DB `iceberg`.
- Namespaces: `bronze` (raw transcripts, booking events, payment events via analytics-pipeline Kafka consumer writing parquet+Iceberg), `silver` (cleaned/deduped Spark jobs), `gold` (dbt marts: daily bookings per tenant, revenue, no-show rate, agent containment rate).
- Trino catalogs: `iceberg` + `postgresql` (federated queries).

## 14. Apps/admin-web (Next.js 15 + TS + Tailwind + shadcn-style components, no Clerk/Convex)
- Pages: `/` marketing, `/sign-in` (Keycloak via OIDC Authorization Code + PKCE using `openid-client`/Auth.js keycloak provider), `/app/[orgSlug]/{overview,bookings,offerings,team,availability,knowledge,public-site,voice-agent,billing,settings}`, `/p/[siteSlug]` public booking page with chat widget + LiveKit voice component.
- Data via APISIX `/api/*` with BFF route handlers; WS to `/ws` for live booking events.

## 15. Quality bar (all subagents)
- Every service: `Dockerfile` (multi-stage), `README.md` (env vars, run instructions), health endpoint `/healthz`, structured logging, graceful shutdown, Dapr client usage where specified, unit-testable core logic (include at least smoke-level tests or a `tests/` note).
- Compose must `docker compose config -q` clean. No placeholders like "TODO: implement" in critical paths — real, coherent code (simplifications allowed and documented in ADRs).
- Language versions: Go 1.23, Rust 1.80+ (edition 2021), Python 3.12.
