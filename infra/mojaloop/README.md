# infra/mojaloop — Mojaloop simulator (SPEC §9)

## What it is
The `mojaloop` container runs `mojaloop/simulator:latest`, which stands in for
the Mojaloop switch in dev. It simulates **FSPIOP-style quoting and transfer
endpoints** (`POST /quotes`, `POST /transfers`, plus parties lookups) that the
payments-service Mojaloop adapter calls for cross-border payout of tenant
earnings — no real money movement, just protocol-faithful responses.

## Contract
- Container port: **8444** (SPEC §3).
- payments-service reaches it via env `MOJALOOP_ENDPOINT=http://mojaloop:8444`
  (SPEC §9).
- Flow used by the adapter:
  1. `POST /quotes` — obtain an ILP condition + transfer amount quote for a payout.
  2. `POST /transfers` — execute the payout against the accepted quote.
- Deposit holds/captures/refunds are **TigerBeetle** transfers (codes 100–104),
  not Mojaloop; Mojaloop is only the external payout rail.

## Notes
- Dev-only; no auth/TLS. For the full reference stack with real scheme-adapters
  and central-ledger, swap to `mojaloop/ml-testing-toolkit` or a mini-loop helm
  deployment and keep the same 8444 contract.
- Volume `mojaloop-data` persists simulator state/rules if the image uses them.
