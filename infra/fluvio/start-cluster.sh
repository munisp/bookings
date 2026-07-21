#!/bin/sh
# start-cluster.sh — dev Fluvio SC + local SPU inside one container (SPEC §5).
# The infinyon/fluvio image ships the `fluvio` CLI (which bundles `fluvio-run`).
# We start the SC on 0.0.0.0:9003, then a local SPU registered against it.
set -e

SC_ADDR="0.0.0.0:9003"

echo "[fluvio] starting SC on ${SC_ADDR}"
fluvio-run sc --local --bind "${SC_ADDR}" &
SC_PID=$!

# Wait for the SC to accept connections before starting the SPU.
i=0
until nc -z 127.0.0.1 9003 2>/dev/null; do
  i=$((i + 1))
  if [ "$i" -gt 30 ]; then
    echo "[fluvio] SC did not come up in time" >&2
    exit 1
  fi
  sleep 1
done

echo "[fluvio] starting local SPU (id 5001, ports 9010/9011)"
fluvio-run spu -i 5001 \
  -p 0.0.0.0:9010 \
  -v 0.0.0.0:9011 \
  --sc 127.0.0.1:9003 &
SPU_PID=$!

touch /tmp/fluvio-started
echo "[fluvio] cluster up (sc=${SC_ADDR})"

trap 'kill ${SC_PID} ${SPU_PID} 2>/dev/null || true' TERM INT
wait
