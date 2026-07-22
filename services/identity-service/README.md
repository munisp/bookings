# identity-service

Tenant provisioning and identity context for OpenDesk (SPEC §7 identity schema,
§8 AuthN/AuthZ). Go 1.23, chi router, pgx/v5 + pgxpool, zap logging.

## Responsibilities

- Public tenant context for agent session injection (name, timezone, currency,
  locale, terminology, plan) — consumed by the voice/conversation services and
  by booking-service's tenant resolver.
- Tenant provisioning: DB row + Keycloak group `/tenants/{slug}` + Permify
  tenant/relationships + `TenantProvisioned` CloudEvent on
  `opendesk.identity.events` via the Dapr pubsub component `pubsub-kafka`.
- Member invites: Keycloak user creation (+ group join), membership row,
  Permify relationship, `MemberInvited` CloudEvent.
- Idempotent internal endpoints for the `TenantOnboardingWorkflow`.

## Endpoints

| Method | Path | Description |
|---|---|---|
| GET | `/healthz` | Liveness (pings Postgres) |
| GET | `/v1/tenants/{slug}` | Public tenant context (incl. `id`) |
| POST | `/v1/tenants` | Provision a tenant |
| GET | `/v1/tenants/{slug}/members` | List memberships |
| POST | `/v1/tenants/{slug}/members` | Invite member (role owner\|admin\|staff\|viewer) |
| POST | `/internal/tenants/{slug}/ensure-group` | Idempotent Keycloak group creation (Temporal onboarding) |
| POST | `/internal/tenants/{slug}/ensure-permify` | Idempotent Permify tenant creation |

## Environment variables

| Var | Default | Description |
|---|---|---|
| `PORT` | `7001` | HTTP listen port |
| `DATABASE_URL` | — (required) | Postgres DSN for the `identity` DB |
| `KEYCLOAK_URL` | `http://keycloak:8080` | Keycloak base URL |
| `KEYCLOAK_REALM` | `opendesk` | Realm |
| `KEYCLOAK_ADMIN_CLIENT_ID` | — | Admin client id (client_credentials) |
| `KEYCLOAK_ADMIN_CLIENT_SECRET` | — | Admin client secret |
| `PERMIFY_URL` | `http://permify:3476` | Permify HTTP API base |
| `DAPR_HOST` | `daprd-identity` | daprd sidecar host |
| `DAPR_HTTP_PORT` | `3500` | daprd HTTP port |
| `DAPR_PUBSUB_NAME` | `pubsub-kafka` | Dapr pubsub component |
| `IDENTITY_EVENTS_TOPIC` | `opendesk.identity.events` | Identity events topic |
| `NOTIFICATION_APP_ID` | `notification` | Dapr app-id of notification-worker (fire-and-forget `POST /dev/trigger-onboarding` after provisioning starts the `TenantOnboardingWorkflow`) |
| `SHUTDOWN_TIMEOUT_SECONDS` | `15` | Graceful shutdown budget |

## Run

```bash
go build ./... && go test ./...
DATABASE_URL=postgres://opendesk:opendesk@localhost:5432/identity \
KEYCLOAK_ADMIN_CLIENT_ID=service-accounts KEYCLOAK_ADMIN_CLIENT_SECRET=... \
  ./server
# or
docker build -t opendesk/identity-service .
```

## Notes / deviations

- **Permify via HTTP API v1** (not gRPC): `POST /v1/tenants/{t}/permissions/check`
  and `/data/relationships/write`. Exported as the `permify.Authorizer`
  interface so checks are mockable; the same pattern is used by
  booking-service.
- Keycloak/Permify failures during `POST /v1/tenants` are logged and deferred
  to the durable `TenantOnboardingWorkflow` (which calls the idempotent
  `/internal/.../ensure-*` endpoints) instead of failing provisioning.
- Realm role `staff` maps to the Permify relation `member` (SPEC §8 schema
  relations: owner/admin/member/viewer).
- CloudEvents 1.0 envelope per SPEC §4: `{specversion, id, source, type,
  subject, time, tenantid, data}`.

## Digital twins (SPEC-W3 §3, innovation 12)

- `POST /internal/tenants/{slug}/twin` creates an ephemeral copy of the
  tenant: slug `{slug}-twin-{6rand}` (base truncated to fit the 63-char slug
  rule), industry/timezone/currency/locale/terminology copied, `plan='twin'`,
  `metadata={"twin_of": "<slug>"}`. Onboarding is triggered exactly like
  `POST /v1/tenants` (same `TenantOnboardingWorkflow`), and a
  `TwinCleanupWorkflow` is armed via notification-worker's
  `POST /dev/trigger-twin-cleanup` (24h timer → Dapr
  `DELETE /v1/tenants/{slug}`).
- `DELETE /v1/tenants/{slug}` deletes a tenant + its memberships.
  **Guard (permify-free by design):** slugs containing `-twin-` delete
  freely — the cleanup workflow calls over the private Dapr mesh and
  operators via the admin UI; every other slug requires the caller (JWT
  `sub` or `X-User-Id`) to hold `manage_catalog` on the organization
  (Permify check).
- **Cascade note:** only the identity rows (tenant + memberships) are
  removed. Twin data in booking/conversation/knowledge expires with the
  twin's 24h lifetime and is reclaimed by those services' own retention —
  twins are short-lived sandboxes, not production tenants.
