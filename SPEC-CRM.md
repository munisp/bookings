# SPEC-CRM.md — Phase 2 Addendum: Twenty CRM Integration + Industry Workflow Packs

Additive to SPEC.md. Existing contracts remain sacred. Repo root: /mnt/agents/output/opendesk (edit in place, NO git).

## A. Twenty CRM (self-hosted, in-compose)
- Images: `twentycrm/twenty:v1.3.2` (pin a real v1.x tag; if unsure use `twentycrm/twenty:latest` with a README note) for both `twenty-api` (NODE_PORT 3000, host 3100) and `twenty-worker` (`command: ["yarn","worker:prod"]`). Dedicated `twenty-redis` container (redis:7-alpine). Postgres: reuse main `postgres` container with a new database `twenty` (add to infra/postgres/init-scripts/00-create-dbs.sql).
- Env (api+worker): PG_DATABASE_URL=postgres://opendesk:opendesk@postgres:5432/twenty, REDIS_URL=redis://twenty-redis:6379, SERVER_URL=http://localhost:3100, FRONT_BASE_URL=http://localhost:3100, ACCESS_TOKEN_SECRET/LOGIN_TOKEN_SECRET/REFRESH_TOKEN_SECRET/FILE_TOKEN_SECRET/APP_SECRET (dev defaults via ${} with -dev fallbacks), MESSAGE_QUEUE_TYPE=bull-mq, STORAGE_TYPE=local.
- APISIX: add route `/crm/*` → twenty-api:3000 with proxy-rewrite strip `/crm/?(.*)` → `/$1`, behind openid-connect bearer_only like other /api routes. Web sidebar links to http://localhost:3100 for the CRM UI.

## B. crm-sync-service (Go 1.23, port 7010, app-id `crm-sync`, daprd-crm-sync sidecar)
Purpose: OpenDesk → Twenty one-way sync (events) + minimal reverse webhook intake.
- Config env: PORT=7010, KAFKA_BROKERS, PG (own DB `crm_sync` — add to 00-create-dbs.sql), TWENTY_API_URL=http://twenty-api:3000, TWENTY_API_KEY (placeholder dev value + runbook instructions to create a real key in Twenty Settings→API & Webhooks), TWENTY_RATE_PER_MIN=90.
- Table `sync_map(id serial, kind text, opendesk_id text, twenty_id text, tenant_id uuid, updated_at timestamptz, UNIQUE(kind,opendesk_id,tenant_id))` — bootstrap DDL on startup, idempotent.
- Consumers (segmentio/kafka-go, consumer group `crm-sync`, DLQ to opendesk.dlq after 3 attempts, poison-pill safe):
  - `opendesk.identity.events` TenantProvisioned → upsert Twenty **Company** {name, domainName: slug + ".opendesk.local"}; store sync_map(kind=tenant).
  - `opendesk.booking.events` BookingCreated/Confirmed → upsert **Person** by contact phone/email (filter query: `?filter=emails.primaryEmail[eq]...` or phone — implement `findPerson` via GET /rest/people?filter=... then POST or PATCH); create **Task** ("{offering} appointment at {starts_at}") linked to person; store sync_map(kind=contact, kind=booking). BookingCancelled → PATCH task status done/cancelled note. Rescheduled → PATCH task dueDate.
  - `opendesk.conversation.events` ToolInvoked(book_appointment) → optional Note on person ("Booked via AI receptionist").
- Twenty client: net/http, Bearer auth, JSON filter encoding, token-bucket rate limiter (90/min), retry w/ backoff on 429/5xx, batch endpoint awareness (60/call) documented.
- HTTP: /healthz, /metrics (counters per event type + twenty call latency), POST /webhooks/twenty (reverse intake: verifies HMAC header X-Twenty-Webhook-Signature with env TWENTY_WEBHOOK_SECRET, logs + emits Kafka opendesk.crm.events CloudEvent — topic to be created; add `opendesk.crm.events` to infra/kafka/create-topics.sh).
- Root compose: `crm-sync` + `daprd-crm-sync` (DAPR_APP_ID=crm-sync, app-port 7010), depends_on kafka, postgres, twenty-api. Add daprd pubsub scope `crm-sync` in infra/dapr/components/pubsub.kafka.yaml.

## C. Industry workflow packs
Directory `industries/` at repo root. Each pack is YAML with EXACT schema:
```yaml
id: salon | clinic | consultancy | support-desk
displayName: ...
terminology: {offering, team_member, booking, contact}   # merged into tenant.terminology
agentPersona: |        # appended to voice agent system prompt
bookingPolicy: {depositPercent: 0-100, noShowFeeCents: int, phoneConfirmation: true, intakeRequired: bool, cancellationWindowHours: int}
temporalWorkflow: SalonDepositWorkflow | ClinicIntakeWorkflow | ConsultancyFollowupWorkflow | SupportEscalationWorkflow
offerings: [{name, duration_min, buffer_min, price_cents, capacity}]
reminders: {offsets: ["24h","1h"], channels: ["email","sms"]}
knowledgeSeed: [{title, body}]
dashboardLabels: {bookingSingular, bookingPlural, customerTerm}
```
Four packs: **salon** (deposit 30%, stylist terminology, prep-note task), **clinic** (intake form required, consent reminder T-72h, HIPAA-ish PII care note in persona, no discounts), **consultancy** (discovery-call free, proposal follow-up task in CRM, invoice reminder), **support-desk** (SLA first-response 4h, escalation timers, ticket terminology).

### C1. identity-service changes
- `tenants` table: add `industry text not null default 'salon'` — idempotent ALTER in identity store bootstrap (and document in 02-identity-schema.sql for fresh installs).
- POST /v1/tenants accepts optional `industry`; validates against pack ids (read packs from mounted `industries/` dir — add volume mount to identity + notification in root compose: `./industries:/industries:ro`, env INDUSTRIES_DIR=/industries).
- GET /v1/tenants/{slug} response gains `industry` + resolved `pack` summary (terminology, bookingPolicy, dashboardLabels) so the voice runtime + web can consume it without loading YAML themselves. Implement a small internal/packs loader (YAML parse via gopkg.in/yaml.v3).
- Onboarding trigger payload gains `industry`.

### C2. notification-worker changes (Temporal)
- `TenantOnboardingWorkflow` gains industry param; new activity `ApplyIndustryPack` → reads pack, Dapr-invokes booking-service to seed offerings + knowledge-service to seed knowledgeSeed docs (idempotent by name), updates tenant terminology via identity internal endpoint (add POST /internal/tenants/{slug}/terminology to identity if missing — coordinate: identity agent owns).
- New workflows (task queue opendesk-main, real code + tests):
  - `ClinicIntakeWorkflow(bookingId, tenantSlug)` — T-72h intake reminder (email w/ form link placeholder), signal `IntakeCompleted`, at T-2h if incomplete → staff alert task.
  - `SalonDepositWorkflow` — verifies deposit hold via payments activity; if booking within cancellation window and no deposit → reminder; on NoShow signal → no-show fee activity.
  - `ConsultancyFollowupWorkflow` — after booking end (timer), send follow-up email + create CRM follow-up Task via Dapr invoke crm-sync (POST /v1/tasks — crm-sync exposes this helper endpoint) + T-7d proposal reminder.
  - `SupportEscalationWorkflow` — SLA timer 4h first response; signal `Responded`; on timeout → escalate (email to tenant owner + priority flag event to opendesk.crm.events).
- `BookingSagaWorkflow` input gains `industry`; after ConfirmBooking, starts the pack's workflow as child (default SalonDeposit if unknown).
- Worker registers all new workflows/activities; tests: intake timeout path, deposit no-show path (use Temporal testsuite like existing).

### C3. booking-service changes
- Booking create payload (and public endpoint) accepts optional `industry`; saga start passes tenant's industry (resolve via tenant row — booking already resolves tenant; add industry to identity response consumption or store on sites table — simplest: identity GET /v1/tenants/{slug} now returns industry; booking's tenant resolver caches it).
- Phone-confirmation + cancellationWindow enforcement already exist; deposit requirement: if pack bookingPolicy.depositPercent > 0, saga input amount = ceil(price * pct). Pass through to HoldDeposit.

### C4. voice-agent-runtime change (small)
- Tenant bootstrap already fetches identity context; if response contains `pack.agentPersona`, append to system prompt. Guard for absence.

### C5. Seed scripts
- `scripts/seed-industries.sh` — creates 4 demo tenants (acme-salon, acme-clinic, acme-consult, acme-support) each with its industry via POST identity:7001/v1/tenants (direct port, like seed-demo.sh).

## D. Web (admin-web) changes
- Settings page: read-only **Industry pack** card (from GET /v1/tenants/{slug}: industry, displayName, bookingPolicy summary, dashboardLabels) + link to CRM (http://localhost:3100) in sidebar.
- Terminology: dashboard pages already use terminology; ensure booking labels fall back to pack dashboardLabels when tenant terminology lacks overrides (client-side merge helper in lib/).
- No new auth flows.

## E. Docs
- docs/integrations/twenty-crm.md — architecture, API key setup, sync mapping table, webhook setup, rate limits, troubleshooting.
- docs/industries.md — pack schema reference, the 4 packs, how onboarding applies them, how to author a new pack.
- docs/runbooks/operations.md — append CRM sync ops section (sync_map inspection, replay from DLQ, rekey procedure).
- README.md — add Twenty + industries to feature list and UI table (CRM UI http://localhost:3100).

## F. Quality bar
Same as SPEC §15. Go services: build+vet+test green (Go at /tmp/go or reinstall 1.23.4, GOPROXY=goproxy.cn). All YAML parse-checked. No placeholders in critical paths.
