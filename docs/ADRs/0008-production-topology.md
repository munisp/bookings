# ADR-0008: Production HA topology

* Status: proposed
* Date: 2025-01 (Wave 3-4)
* Supersedes: nothing. Complements ADR-0007 (dev simplifications) — this ADR
  describes the production target; the single-node compose stack remains the
  dev topology.

## Context

The dev compose stack runs every stateful system as a single container with
`restart: unless-stopped`. That is fine for demos and CI, but loses data or
availability on any single-node failure. This ADR pins the production
topology per system so infrastructure work converges on one design instead of
per-incident improvisation. Open-source-first rule applies: no managed SaaS
dependencies (SPEC §1).

## Decision

### Kafka — 3 brokers, rf=3

- 3 KRaft controllers+brokers (combined mode acceptable up to ~5 nodes;
  dedicated controllers beyond that).
- All `opendesk.*` topics: `replication.factor=3`, `min.insync.replicas=2`,
  producers `acks=all` (dev topics created by
  `infra/kafka/create-topics.sh` use rf=1 — override at provisioning time in
  prod, not by editing the dev script).
- Survives 1 broker loss with no data loss; 2 losses = unavailability, not
  divergence.
- MirrorMaker2 (`deploy/k3s/mirror-maker2.yaml`) provides store-and-forward
  from edge appliances to the central cluster for `opendesk.transcripts-raw`.

### Postgres — Patroni, 3 nodes

- Patroni + etcd (3-node etcd, or reuse the existing DCS if one exists) with
  streaming replication; synchronous commit for the `booking` and
  `tigerbeetle-adjacent` metadata DBs (`synchronous_mode: quorum`),
  asynchronous for the rest.
- PgBouncer in front; services keep their per-service roles
  (`app_booking`/`app_conversation`/`app_knowledge`, RLS unchanged — roles
  and policies replicate).
- Backups: WAL-G/pgBackRest to the MinIO/S3 `lake`-adjacent backup bucket,
  replacing the dev per-DB `pg_dump` cron (infra/backups) for prod.
- `iceberg` (REST catalog) DB lives in the same cluster; it is small and
  must be point-in-time recoverable *with* the object store.

### TigerBeetle — 3-node cluster

- Cluster of 3 replicas (`--replica-count=3`, replicas 0..2), replacing the
  dev single replica (`--development`). TB consensus (Viewstamped
  Replication) tolerates 1 replica loss; data files are per-replica.
- Replication, not backups, is the durability story; still snapshot one
  replica's data file off-box (see infra/backups) rather than pausing a
  leader.
- payments-service `LedgerClient` gets a multi-address `TB_ADDRESSES` list;
  no code change, config only.

### Temporal — multi-node

- 3 matching/history/frontend service sets (each role at least 2 replicas),
  workers (notification-worker) at 2+ replicas — workers are stateless, the
  task queue (`opendesk-main`) handles failover.
- Persistence on the Patroni cluster (dedicated `temporal` +
  `temporal_visibility` DBs); keep the `opendesk` namespace.
- Workflows are durable by design; node loss only delays in-flight
  activities, which retry per their retry policies.

### Permify — HA

- 2+ stateless replicas behind the internal LB, shared Postgres backend
  (`permify` DB on Patroni). Schema is versioned and loaded at startup;
  rolling deploys must serialize schema migrations (one replica migrates,
  others join).
- Authorization checks are on the hot path of every tenant API call — cache
  TTLs stay short; a Permify outage degrades to fail-closed (documented in
  the secrets/ops runbooks).

### Keycloak — HA

- 2+ replicas with `cache-ispn` (embedded Infinispan) clustering, shared
  `keycloak` DB on Patroni, sticky sessions at the gateway for the admin
  console only (token endpoints are stateless).
- Realm `opendesk` exported to Git and re-importable; treat it as code.

### Everything stateless

- All 10 app services (identity … crm-sync), APISIX (standalone,
  file-driven), gateway-edge: 2+ replicas, no local state, horizontal
  scaling only.

## Sizing table (single production cell, ~200 tenants / ~50k bookings/mo)

| Component | Count | vCPU | RAM | Storage | Notes |
| --- | --- | --- | --- | --- | --- |
| Kafka broker | 3 | 4 | 8 GB | 500 GB SSD | rf=3, 7-day retention |
| Postgres (Patroni) | 3 | 4 | 16 GB | 250 GB SSD | + 3-node etcd (1 vCPU/2 GB each) |
| TigerBeetle | 3 | 2 | 4 GB | 100 GB NVMe | data file grows with ledger volume |
| Temporal (all roles) | 3 | 2 | 4 GB | — | state in Postgres |
| Permify | 2 | 1 | 2 GB | — | |
| Keycloak | 2 | 2 | 4 GB | — | |
| App services (each of 10) | 2 | 1 | 1 GB | — | voice-agent-runtime: 2 vCPU/4 GB if in-process STT |
| APISIX | 2 | 2 | 2 GB | — | + etcd if Admin-API mode adopted |
| OpenSearch | 3 | 4 | 8 GB (4 GB heap) | 500 GB SSD | `conversations`/`kb-chunks` indices |
| MinIO | 4 | 2 | 4 GB | 1 TB × 4 (EC:2) | lake + exports + backups buckets |
| Observability (Prom/Loki/Grafana/OTel) | 2 | 4 | 8 GB | 200 GB | 15-day metric, 7-day log retention |

Edge appliance (k3s, `deploy/k3s/`): 1× 4 vCPU / 8 GB node runs the core
services at reduced scale with MirrorMaker2 store-forward; it is a
degraded-mode outpost, not part of the HA cell.

## Consequences

- ~25 small VMs (or equivalent) for one HA cell; the dev compose topology is
  unchanged.
- New operational surface: Patroni failovers, TB cluster upgrades (rolling,
  one replica at a time), Kafka partition reassignment on broker loss.
- All secrets move out of compose env into SOPS/Vault (runbook:
  docs/runbooks/secrets.md).
- Deferred: multi-region active-active; cross-region Kafka and TB remain
  single-region with async replicas for DR only.
