#!/usr/bin/env bash
# deploy.sh — build + load the pii-redact smartmodule and deploy the
# kafka-sink connector (opendesk.transcripts-raw -> Kafka
# opendesk.conversation.transcripts, SPEC §5 / SPEC-W3 §1).
#
# Prereqs: the dev compose stack is up (fluvio + fluvio-topics + kafka).
# Smartmodule build uses `smdk` when installed, otherwise a rust container
# with the wasm32-wasip1 target (same artifact). The connector is deployed
# with `cdk deploy start` when the CDK CLI is available; otherwise the
# script stops after loading the smartmodule and prints the exact next step
# (the connector runtime is k8s/CDK — see README).
set -euo pipefail

cd "$(dirname "$0")"
SM_DIR="pii-redact"
SM_NAME="pii-redact"
SM_QUALIFIED="opendesk/pii-redact@0.1.0"
WASM_REL="target/wasm32-wasip1/release/pii_redact.wasm"
WASM="$SM_DIR/$WASM_REL"
CONNECTOR_CONFIG="kafka-sink-connector.yaml"
SC_HOST="${FLUVIO_SC_HOST:-localhost:9003}"
SC_HOST_IN_CLUSTER="${FLUVIO_SC_HOST_IN_CLUSTER:-fluvio:9003}"

echo "== 1/4 build smartmodule ($SM_NAME) =="
if command -v smdk >/dev/null 2>&1; then
  (cd "$SM_DIR" && smdk build)
elif command -v cargo >/dev/null 2>&1 && rustup target list --installed 2>/dev/null | grep -q wasm32-wasip1; then
  echo "smdk not found; using cargo --target wasm32-wasip1 (equivalent artifact)"
  (cd "$SM_DIR" && cargo build --release --target wasm32-wasip1)
elif command -v docker >/dev/null 2>&1; then
  echo "smdk/cargo not found; building in rust:1 container"
  docker run --rm -v "$PWD/$SM_DIR:/work" -w /work rust:1-bookworm \
    sh -c 'rustup target add wasm32-wasip1 && cargo build --release --target wasm32-wasip1'
else
  echo "no smdk, cargo+wasm target, or docker available to build the smartmodule" >&2
  exit 1
fi
[ -f "$WASM" ] || { echo "expected artifact missing: $WASM" >&2; exit 1; }
ls -la "$WASM"

echo "== 2/4 load smartmodule into the dev cluster =="
if command -v fluvio >/dev/null 2>&1; then
  fluvio profile add docker "$SC_HOST" docker 2>/dev/null || true
  fluvio profile switch docker 2>/dev/null || true
  fluvio smartmodule create "$SM_NAME" --wasm-file "$WASM" || \
    fluvio smartmodule update "$SM_NAME" --wasm-file "$WASM"
else
  # Run the CLI from the fluvio image against the in-cluster SC.
  docker run --rm --network opendesk \
    -v "$PWD/$SM_DIR:/work" -w /work \
    infinyon/fluvio:latest sh -c "
      fluvio profile add docker '$SC_HOST_IN_CLUSTER' docker 2>/dev/null || true
      fluvio profile switch docker 2>/dev/null || true
      fluvio smartmodule create '$SM_NAME' --wasm-file '$WASM_REL' || \
        fluvio smartmodule update '$SM_NAME' --wasm-file '$WASM_REL'
      fluvio smartmodule list
    "
fi

echo "== 3/4 verify Kafka sink topic exists =="
docker exec kafka /opt/bitnami/kafka/bin/kafka-topics.sh \
  --bootstrap-server localhost:9092 --describe \
  --topic opendesk.conversation.transcripts >/dev/null 2>&1 || {
    echo "topic opendesk.conversation.transcripts missing; running kafka topic init"
    docker compose run --rm kafka-topics
  }

echo "== 4/4 deploy kafka-sink connector =="
if command -v cdk >/dev/null 2>&1; then
  cdk deploy start -c "$CONNECTOR_CONFIG"
  cdk deploy list
else
  cat >&2 <<EOF
WARNING: 'cdk' (Fluvio Connector Development Kit) not found — smartmodule is
loaded, but the connector was NOT started. Install cdk and run:

  cdk deploy start -c infra/fluvio/$CONNECTOR_CONFIG

or, on a k8s-hosted Fluvio cluster, apply the same config via the cluster's
connector mechanism. The connector config is ready at
infra/fluvio/$CONNECTOR_CONFIG (applies smartmodule $SM_QUALIFIED,
topic opendesk.transcripts-raw -> kafka opendesk.conversation.transcripts).
EOF
fi

echo "deploy.sh done"
