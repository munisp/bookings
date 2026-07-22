# tests/e2e — full-platform end-to-end suite

Real pytest suite that drives the whole OpenDesk compose stack. **It only
runs on a docker host** — there are no mocks; every assertion hits a live
service, Kafka, Postgres, OpenSearch, or Trino.

## What it covers (SPEC-W3 §1)

`test_full_flow.py` walks one tenant through the entire platform in order:

1. **provision** — `POST /v1/tenants` on identity (kicks off the
   `TenantOnboardingWorkflow`, which seeds the default public site);
2. **seed** — offering, team member, and weekly availability rules through
   the booking tenant API (`X-Tenant-Slug`, same pattern as
   `scripts/seed-demo.sh`);
3. **availability** — slots via the gateway's anonymous public route;
4. **public book** — `POST /api/bookings/public/sites/{slug}/bookings`;
5. **saga events** — booking reaches `confirmed` (Temporal saga) and a
   CloudEvent with the booking id appears on `opendesk.booking.events`;
6. **crm sync_map** — crm-sync records a `sync_map` row in the `crm_sync`
   Postgres DB for the booking;
7. **opensearch doc** — a `/voice/chat` turn is indexed into the
   `conversations` index;
8. **lakehouse bronze row** — the booking event is queryable in
   `iceberg.bronze.booking_events` via Trino.

Steps 5–8 poll with generous timeouts because they cross async boundaries
(outbox dispatcher, Temporal workers, Kafka consumers, Iceberg commits).
Steps that need optional pieces of the stack (crm-sync/Twenty, the LLM
backend for chat, Trino/Iceberg) **skip with an explicit reason** when that
component isn't up, instead of failing spuriously.

## Running locally

```bash
# 1. Bring the full stack up (takes a while on first build)
make up

# 2. Run the suite (expects the stack already up)
pip install -r tests/e2e/requirements.txt
python -m pytest tests/e2e/ -v
```

Or let the fixture manage the stack lifecycle itself:

```bash
E2E_COMPOSE_UP=1 E2E_COMPOSE_DOWN=1 python -m pytest tests/e2e/ -v
```

Without a docker host the suite **skips cleanly** (the session fixture calls
`pytest.skip`), so collecting it on a non-docker CI runner is safe.

## Environment knobs

| Var | Default | Meaning |
| --- | --- | --- |
| `E2E_COMPOSE_UP` | unset | `1` → fixture runs `docker compose up -d --build` first |
| `E2E_COMPOSE_DOWN` | unset | `1` → tear the stack down after the run |
| `E2E_HEALTH_TIMEOUT` | `600` | seconds to wait for all `/healthz` |
| `E2E_GW` | `http://localhost:9080` | APISIX gateway base URL |
| `E2E_IDENTITY` … `E2E_ANALYTICS` | `http://localhost:7001…7009` | direct service URLs |
| `E2E_CRM_SYNC` | `http://localhost:7010` | crm-sync health endpoint |
| `E2E_OPENSEARCH` | `http://localhost:9200` | OpenSearch |
| `E2E_TRINO` | `http://localhost:8088` | Trino HTTP API |

## CI wiring

`.github/workflows/ci.yml` has an `e2e` job that runs this suite with
`E2E_COMPOSE_UP=1`. It is gated on `workflow_dispatch` (full stack build is
too heavy for every PR); flip the `if:` to a self-hosted runner label or a
nightly `schedule:` when a suitable runner exists.
