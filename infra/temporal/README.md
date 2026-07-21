# infra/temporal — Temporal dev server (SPEC §6)

## Containers (see `../docker-compose.core.yml`)
- **`temporal`** — `temporalio/auto-setup:1.24`. On start it:
  - creates the `temporal` Postgres DB schema (persistence **and** visibility
    both use the `temporal` DB in the shared `postgres` container — dev simplification),
  - registers namespace **`opendesk`** (env `NAMESPACE`, `SKIP_NAMESPACE_CREATION=false`),
  - serves gRPC on **7233**.
- **`temporal-ui`** — web UI on **8233**.

## Contracts used by services
- Namespace: `opendesk`
- Task queue: **`opendesk-main`**
- Workflows (SPEC §6): `BookingSagaWorkflow` (saga: ReserveSlot → HoldDeposit →
  ConfirmBooking → SendConfirmation; compensations ReleaseSlot, VoidHold),
  `ReminderWorkflow` (T-24h/T-1h timers), `NoShowFollowupWorkflow`,
  `TenantOnboardingWorkflow`.
- Workers connect to `temporal:7233`. The Go worker lives in
  `services/notification-worker`; booking/payments activities are invoked over
  Dapr service invocation HTTP.

## Files
- `dynamicconfig/development.yaml` — minimal dynamic config mounted into the
  auto-setup container at `/etc/temporal/config/dynamicconfig`.
