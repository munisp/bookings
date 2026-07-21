#!/usr/bin/env bash
# Create the raw transcripts topic consumed by gateway-edge and the sinks
# (SPEC §4: 6 partitions, rf 1 in dev; SPEC §5).
set -euo pipefail

TOPIC="${TRANSCRIPTS_TOPIC:-opendesk.transcripts-raw}"
PARTITIONS="${PARTITIONS:-6}"
REPLICATION="${REPLICATION:-1}"

if ! command -v fluvio >/dev/null 2>&1; then
  echo "fluvio CLI not found in PATH" >&2
  exit 1
fi

if fluvio topic list | awk '{print $1}' | grep -qx "$TOPIC"; then
  echo "topic $TOPIC already exists"
else
  fluvio topic create "$TOPIC" --partitions "$PARTITIONS" --replication "$REPLICATION"
  echo "created topic $TOPIC (partitions=$PARTITIONS replication=$REPLICATION)"
fi

fluvio topic list
