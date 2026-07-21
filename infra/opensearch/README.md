# OpenSearch (OpenDesk search & RAG tier — SPEC §10)

Runs inside `infra/docker-compose.edge.yml`:

- `opensearch` — single-node dev cluster on **9200** (`discovery.type=single-node`,
  security plugin **disabled** for dev, 512 MB heap).
- `opensearch-dashboards` — UI on **5601** (security dashboards plugin disabled).
- `opensearch-init` — one-shot job that runs `setup-indices.sh` once the cluster is healthy.

## Bootstrap

```bash
docker compose -f infra/docker-compose.edge.yml up -d opensearch opensearch-dashboards
docker compose -f infra/docker-compose.edge.yml up opensearch-init   # runs setup-indices.sh
# or against a running cluster directly:
OS_HOST=http://localhost:9200 ./setup-indices.sh
```

The script is idempotent and creates:

| Object | Notes |
|---|---|
| pipeline `pii-safe` | gsub-based email/phone scrub on `text` + sets `redacted=true`. This is **defense in depth**: the primary redaction point is the Fluvio `pii-redact` smart module (SPEC §5) upstream of indexing. |
| `kb-chunks` | k-NN index: `embedding` = `knn_vector`, **dimension 384** (sentence-transformers `all-MiniLM-L6-v2`), HNSW on the **lucene** engine, `cosinesimil`; plus `content`/`title` text and `tenant_id`/`document_id` keywords for tenant-filtered hybrid search (BM25 + k-NN, RRF — done by knowledge-service). |
| `conversations` | PII-redacted transcript turns: `tenant_id`, `conversation_id`, `role`, `channel` keywords; `text`; `redacted` boolean; `ts` date. Default pipeline `pii-safe`. |
| `bookings-analytics` | Synced from lakehouse gold marts: `tenant_id`, `date`, `bookings_total`, `bookings_confirmed`, `bookings_cancelled`, `no_shows`, `revenue_cents`, `containment_rate`. |

## ISM / lifecycle notes

- `setup-indices.sh` installs ISM policy **`conversations-90d`**: hot → (30d) → read-only
  "warm" → (90d) → delete. Transcripts carry personal data, so retention is deliberately
  short; long-term analytics live in the Iceberg lakehouse, not in OpenSearch.
- The policy auto-attaches to `conversations*` via the policy's `ism_template` block
  (indices created *after* the policy). For pre-existing indices attach manually:

  ```bash
  curl -X POST http://localhost:9200/_plugins/_ism/add/conversations \
    -H 'Content-Type: application/json' -d '{"policy_id": "conversations-90d"}'
  ```

- `kb-chunks` has **no** ISM policy: it is fully rebuildable from the `knowledge`
  Postgres DB, so lifecycle = reindex on schema change (create `kb-chunks-v2`, bulk
  reindex, flip alias).
- `bookings-analytics` rows are idempotent upserts keyed by `(tenant_id, date)` from the
  lakehouse sync job; retention is not time-based.

## Prod hardening (not for dev)

- Re-enable the security plugin (TLS + internal users / OIDC via Keycloak), set
  `OPENSEARCH_INITIAL_ADMIN_PASSWORD`, remove `DISABLE_SECURITY_PLUGIN`.
- 3 dedicated master nodes, `number_of_replicas: 1`, raise heap to 50% of RAM (≤ 32 GB).
- Snapshot repository to MinIO (`repository-s3` plugin) for `conversations*` backup.
