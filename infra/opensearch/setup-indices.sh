#!/bin/sh
# OpenDesk — OpenSearch bootstrap (SPEC §10).
# Idempotent: safe to re-run. Creates:
#   - ingest pipeline `pii-safe`      (defense-in-depth PII scrub for transcript text)
#   - index templates + indices:      kb-chunks (384-dim k-NN), conversations, bookings-analytics
#   - ISM policy `conversations-90d`  (see README for lifecycle notes)
#
# Usage: OS_HOST=http://opensearch:9200 ./setup-indices.sh
set -eu

OS_HOST="${OS_HOST:-http://localhost:9200}"

req() { # req METHOD PATH [BODY]
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -fsS -X "$method" "$OS_HOST$path" -H 'Content-Type: application/json' -d "$body"
  else
    curl -fsS -X "$method" "$OS_HOST$path"
  fi
  echo
}

echo ">> waiting for OpenSearch at $OS_HOST ..."
i=0
until curl -fsS "$OS_HOST/_cluster/health" | grep -qE '"status":"(green|yellow)"'; do
  i=$((i+1)); [ "$i" -gt 60 ] && { echo "cluster not ready after 120s"; exit 1; }
  sleep 2
done
echo ">> cluster is up"

# ---------------------------------------------------------------------------
# Ingest pipeline: pii-safe
# The Fluvio `pii-redact` smart module is the primary redaction point (SPEC §5);
# this pipeline is a defense-in-depth second pass applied at index time.
# ---------------------------------------------------------------------------
echo ">> creating ingest pipeline pii-safe"
req PUT /_ingest/pipeline/pii-safe '{
  "description": "Defense-in-depth PII scrubbing (email/phone) for text fields; primary redaction happens in the Fluvio pii-redact smart module.",
  "processors": [
    {"gsub": {"field": "text", "pattern": "[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}", "replacement": "[REDACTED_EMAIL]", "ignore_missing": true}},
    {"gsub": {"field": "text", "pattern": "\\+?[0-9][0-9 .()\\-]{7,}[0-9]", "replacement": "[REDACTED_PHONE]", "ignore_missing": true}},
    {"set": {"field": "redacted", "value": true, "ignore_missing": false}}
  ]
}'

# ---------------------------------------------------------------------------
# Index templates
# ---------------------------------------------------------------------------
echo ">> creating index template kb-chunks-template"
req PUT /_index_template/kb-chunks-template '{
  "index_patterns": ["kb-chunks*"],
  "priority": 100,
  "template": {
    "settings": {
      "number_of_shards": 1,
      "number_of_replicas": 0,
      "index.knn": true,
      "index.knn.algo_param.ef_search": 100
    },
    "mappings": {
      "properties": {
        "embedding": {
          "type": "knn_vector",
          "dimension": 384,
          "method": {
            "name": "hnsw",
            "engine": "lucene",
            "space_type": "cosinesimil",
            "parameters": {"m": 16, "ef_construction": 128}
          }
        },
        "content": {"type": "text"},
        "title": {"type": "text", "fields": {"keyword": {"type": "keyword", "ignore_above": 256}}},
        "tenant_id": {"type": "keyword"},
        "document_id": {"type": "keyword"},
        "chunk_seq": {"type": "integer"},
        "source_url": {"type": "keyword"},
        "created_at": {"type": "date"}
      }
    }
  }
}'

echo ">> creating index template conversations-template"
req PUT /_index_template/conversations-template '{
  "index_patterns": ["conversations*"],
  "priority": 100,
  "template": {
    "settings": {"number_of_shards": 1, "number_of_replicas": 0, "index.default_pipeline": "pii-safe"},
    "mappings": {
      "properties": {
        "tenant_id": {"type": "keyword"},
        "conversation_id": {"type": "keyword"},
        "site_slug": {"type": "keyword"},
        "channel": {"type": "keyword"},
        "role": {"type": "keyword"},
        "text": {"type": "text"},
        "audio_url": {"type": "keyword"},
        "redacted": {"type": "boolean"},
        "ts": {"type": "date"}
      }
    }
  }
}'

echo ">> creating index template bookings-analytics-template"
req PUT /_index_template/bookings-analytics-template '{
  "index_patterns": ["bookings-analytics*"],
  "priority": 100,
  "template": {
    "settings": {"number_of_shards": 1, "number_of_replicas": 0},
    "mappings": {
      "properties": {
        "tenant_id": {"type": "keyword"},
        "date": {"type": "date"},
        "bookings_total": {"type": "integer"},
        "bookings_confirmed": {"type": "integer"},
        "bookings_cancelled": {"type": "integer"},
        "no_shows": {"type": "integer"},
        "revenue_cents": {"type": "long"},
        "currency": {"type": "keyword"},
        "containment_rate": {"type": "double"},
        "synced_at": {"type": "date"}
      }
    }
  }
}'

# ---------------------------------------------------------------------------
# Concrete indices (templates apply on creation)
# ---------------------------------------------------------------------------
for idx in kb-chunks conversations bookings-analytics; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "$OS_HOST/$idx")
  if [ "$code" = "404" ]; then
    echo ">> creating index $idx"
    req PUT "/$idx"
  else
    echo ">> index $idx already exists (HTTP $code), skipping"
  fi
done

# ---------------------------------------------------------------------------
# ISM policy: conversations-90d — warm after 30d (read-only), delete after 90d.
# Attach by adding "opendesk.plugins.index_state_management.policy_id": "conversations-90d"
# to the conversations template settings, or via the Dashboards UI. See README.
# ---------------------------------------------------------------------------
echo ">> creating ISM policy conversations-90d"
req PUT /_plugins/_ism/policies/conversations-90d '{
  "policy": {
    "description": "Conversation transcript retention: read-only after 30 days, deleted after 90 days (privacy minimisation).",
    "default_state": "hot",
    "states": [
      {"name": "hot", "actions": [], "transitions": [{"state_name": "warm", "conditions": {"min_index_age": "30d"}}]},
      {"name": "warm", "actions": [{"read_only": {}}], "transitions": [{"state_name": "delete", "conditions": {"min_index_age": "90d"}}]},
      {"name": "delete", "actions": [{"delete": {}}], "transitions": []}
    ],
    "ism_template": {"index_patterns": ["conversations*"], "priority": 100}
  }
}'

echo ">> OpenSearch bootstrap complete"
req GET /_cat/indices?v
