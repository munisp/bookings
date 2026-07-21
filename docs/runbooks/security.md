# Security runbook

Covers the SPEC-W3 §2 controls: WAF (open-appsec) posture changes, Postgres
RLS enforcement, gateway rate limiting/authz, and the ZAP baseline scan.

---

## 1. open-appsec: detect → prevent promotion

The open-appsec nano agent (profile `appsec`, see
`infra/docker-compose.edge.yml`) watches `infra/openappsec/local_policy.yaml`
and hot-reloads it — no restarts needed for any step below.

### 1.1 Learning period (detect-learn)

- Default posture is `detect-learn` everywhere: events are logged, nothing
  is blocked. Keep this for **at least one week of representative traffic**
  (or a full run of `tests/e2e/` + `scripts/smoke-test.sh`) per environment.
- The contextual ML engine builds one model per `specific-rules` host entry
  (`localhost/api`, `localhost/p`, `localhost`).
- Inspect detections (logs stay local; `cloud: false`):

  ```sh
  docker logs opendesk-openappsec 2>&1 | grep -i "detect" | tail -50
  ```

- Triage false positives BEFORE enforcing: narrow a `specific-rules` host,
  or tune `minimum-confidence` in `opendesk-web-attack-practice`.

### 1.2 Enforce switch (prevent-learn)

1. In `infra/openappsec/local_policy.yaml` set
   `policies.default.mode: prevent-learn` and the same on each specific rule
   you want enforced (practices inherit via `override-mode: as-top-level`).
2. Raise `opendesk-web-attack-practice.web-attacks.minimum-confidence` to
   `high` so borderline traffic is logged, not blocked.
3. Save — the agent hot-reloads. Watch `prevent-events` in the logs for an
   hour and be ready to flip back (step 1.1 values) as an instant rollback.

### 1.3 API discovery + OpenAPI schema upload

While learning, open-appsec auto-discovers the API surface of `/api/*`
(paths, methods, parameter names/types). To validate requests against the
**authoritative** spec instead of the learned one:

1. Mount the specs (kept in `docs/api/openapi/`: `booking.yaml`,
   `identity.yaml`, `payments.yaml`) into the agent container in
   `infra/docker-compose.edge.yml`:

   ```yaml
   volumes:
     - ../docs/api/openapi:/ext/openapi:ro
   ```

2. Point the discovery practice at them and enforce:

   ```yaml
   # in opendesk-api-discovery-practice
   openapi-schema-validation:
     override-mode: prevent-learn
     files:
       - /ext/openapi/booking.yaml
       - /ext/openapi/identity.yaml
       - /ext/openapi/payments.yaml
   ```

3. Requests whose shape contradicts the uploaded schema are now blocked;
   schema drift on covered endpoints shows up as prevent events.

---

## 2. Postgres row-level security (RLS)

- Every tenant table in the booking/conversation/knowledge schemas has
  `ENABLE` + `FORCE ROW LEVEL SECURITY` with policy
  `tenant_id = current_setting('app.tenant_id', true)::uuid`
  (init scripts 01/03/04).
- Application stores set the tenant per transaction:
  - **booking-service (Go)**: `internal/store.withTenant` opens a tx and runs
    `SELECT set_config('app.tenant_id', $1, true)` before any statement. All
    tenant-scoped queries go through it. Documented exceptions:
    schema bootstrap DDL, the cross-tenant outbox dispatcher
    (`FetchUnsentOutbox`/`MarkOutboxSent`), and public site-slug resolution
    (`GetSiteBySlug` — tenant unknown until the slug resolves; all queries
    after resolution are tenant-scoped).
  - **conversation-service (Python)**: `Database._tenant_tx` (same
    `set_config(..., true)` pattern) — verified, already compliant.
- **Per-service DB roles** (`infra/postgres/init-scripts/05-app-roles.sql`):
  NOLOGIN group roles `app_booking` / `app_conversation` / `app_knowledge`
  hold per-database grants; LOGIN variants `app_*_login` inherit them. The
  superuser `opendesk` bypasses RLS — services must connect with their
  LOGIN role for FORCE RLS to actually bind. Wire-up:
  `BOOKING_PG_USER`/`BOOKING_PG_PASS` in `.env` (see `.env.example`), consumed
  by the booking `DATABASE_URL` construction in `docker-compose.yml` (and by
  booking-service's config fallback `PG_DSN`+`PG_USER`/`PG_PASS`).
- Verify enforcement manually:

  ```sh
  psql "postgres://app_booking_login:app_booking_dev_password@localhost:5432/booking" \
    -c "SELECT count(*) FROM bookings"   -- 0 rows: no app.tenant_id set
  ```

---

## 3. Gateway authz & rate limiting (APISIX)

- `/api/*` routes: `openid-connect` bearer_only against Keycloak + redis
  `limit-count` 600/min per IP.
- `/voice/*` (public anonymous chat/session): no OIDC possible — instead a
  stricter 30/min per-IP `limit-count`. A Turnstile-style bot gate in front
  of `/voice/chat` is the documented prod follow-up (see the comment on the
  `voice-runtime` route in `infra/apisix/apisix.yaml`).
- `/ws` stays on **in-app JWT** (gateway-edge validates against Keycloak
  JWKS): browsers can't set headers on WebSocket upgrades, and the edge must
  bind tenant claims to channels before subscribing. Rationale is documented
  on the `ws-gateway` route.
- Plan-tier quotas: example route `api-plan-tier-example` (disabled) shows
  `limit-count` keyed on `http_x_tenant_plan` with the documented plan map
  `free=60/min`, `pro=600/min` (clone per plan with a `vars` match).

---

## 4. OWASP ZAP baseline scan

`scripts/security-scan.sh` runs the ZAP baseline against the gateway
(`http://host.docker.internal:9080`) in Docker and writes an HTML report to
`reports/`. See the script header for usage and CI wiring. Baseline findings
are informational — triage into issues; do not gate CI on it until the false
positive list is maintained.

---

## 5. GDPR data-subject requests (innovation 13)

- `POST /v1/privacy/export` and `POST /v1/privacy/erase` on booking-service
  (Permify `manage_bookings`) start `GdprExportWorkflow` /
  `GdprEraseWorkflow` (notification-worker).
- Export collects bookings (`?contact=`), conversations (`?contact=`), the
  tenant ledger balance, and the Twenty person (`/v1/people/lookup`) into a
  JSON bundle uploaded to the MinIO `exports` bucket (plain S3 PUT); the
  workflow result is the object path (presigned URLs are a prod add-on).
- Erase publishes a `PrivacyEraseRequested` tombstone CloudEvent to
  `opendesk.privacy.events`; booking (anonymizes contacts), conversation
  (deletes turns) and crm-sync (deletes the Twenty person + sync_map rows)
  consume it. Tombstones are idempotent; replays are safe.
