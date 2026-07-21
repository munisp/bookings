#!/bin/sh
# setup-topics.sh — create Fluvio topics for OpenDesk (SPEC §5).
# Runs in the `fluvio-topics` sidecar (infinyon/fluvio image) against the SC
# in the `fluvio` container. Safe to re-run (creation is tolerant of existing topics).
set -e

SC="${FLUVIO_SC_HOST:-fluvio:9003}"
export FLUVIO_CLOUD_PROFILE=local

echo "[fluvio-topics] targeting SC at ${SC}"

# Point the CLI profile at the in-cluster SC (idempotent).
fluvio profile add docker "${SC}" docker 2>/dev/null || true
fluvio profile switch docker 2>/dev/null || true

echo "[fluvio-topics] waiting for SC..."
i=0
until fluvio cluster status >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 60 ]; then
    echo "[fluvio-topics] SC at ${SC} not reachable" >&2
    exit 1
  fi
  sleep 2
done

echo "[fluvio-topics] creating opendesk.transcripts-raw (partitions=6 rf=1)"
fluvio topic create opendesk.transcripts-raw --partitions 6 --replication 1 || true

fluvio topic list
echo "[fluvio-topics] done"
