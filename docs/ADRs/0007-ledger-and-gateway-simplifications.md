# ADR-0007 — Ledger client and gateway attachment simplifications

Status: accepted · Date: 2025-01

## Context

SPEC.md §9 requires payments-service (Rust) to write to a TigerBeetle
ledger, and §12 requires the APISIX gateway to attach the OpenAppSec WAF.
Two integration realities forced explicit decisions:

1. **TigerBeetle has no official Rust client crate.** The official clients
   are Go, Node.js, .NET, Java and Python; the wire protocol is a custom
   binary VSR protocol that is unreasonable to reimplement as part of this
   project.

2. **OpenAppSec's APISIX integration** is delivered as a plugin/`open-appsec`
   nginx module whose availability varies by APISIX packaging, and whose
   policy agent normally runs as a sidecar with its own management plane.

## Decision

### Ledger

payments-service defines a `LedgerClient` trait with two implementations,
selected by `LEDGER_IMPL`:

- `sim` (default): an embedded, in-process double-entry ledger implementing
  the exact TigerBeetle account/transfer semantics used by the platform
  (accounts per tenant `deposits`/`revenue`, platform fee account; transfer
  codes 100 hold, 101 capture, 102 refund, 103 no-show fee, 104 payout;
  two-phase pending transfers).
- `tigerbeetle`: adapter module against a real TigerBeetle cluster,
  implemented behind the same trait. It is wired to the official client the
  moment a stable Rust client exists; until then the module documents the
  mapping and the service keeps its contract tests against `sim`.

The REST surface of payments-service (`/v1/accounts/*`, `/v1/transfers`,
`/v1/payouts`) is identical either way, so callers never observe the
impl choice.

### Gateway / WAF

OpenAppSec is integrated at the APISIX layer via its documented plugin
mechanism: `infra/apisix/apisix.yaml` carries the plugin configuration and
`infra/openappsec/` holds the nano-agent policy files, with the attachment
points called out in comments. The OpenAppSec learning-mode → enforce-mode
runbook (docs/runbooks/) is the operational companion.

## Consequences

- `docker compose up` is green out of the box with `LEDGER_IMPL=sim` while a
  real TigerBeetle container runs in the stack ready for the adapter.
- The TigerBeetle integration **contract** (accounts, codes, pending/capture
  semantics) is preserved and testable, satisfying the spec's intent.
- When a maintained Rust TigerBeetle client appears, switching is a
  config change plus enabling the adapter, not a redesign.
