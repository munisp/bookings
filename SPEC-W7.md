# SPEC-W7 — Public-Safety Verticals, QR Payments, Rust Billing Engine, Role-Based Dashboards, Marketing Site

Wave 7 contract. Working repo: `/mnt/agents/output/opendesk` (flaky FUSE — work in `/tmp`, rsync back ADDITIVELY, md5-verify every written file).

## Part A — Public-safety packs (Agent A)

Three new packs following the existing schema (see `industries/government.yaml`, `industries/nigeria-sme.yaml`; validate with `python3 scripts/validate_pack.py validate <file>`):

1. `law-enforcement.yaml` — police/non-emergency crime reporting. Offerings: non-emergency crime report intake
   (theft/burglary/criminal damage, past-tense), statement appointment, case follow-up call, victim-support
   referral session, anonymous tip line intake. HARD RULES (agentPersona + knowledgeSeed):
   - Life-threatening/in-progress emergencies → ALWAYS instruct caller to dial the national emergency number
     (112/911/999 per locale) FIRST; the agent NEVER dispatches officers and NEVER promises response times.
   - Never give legal advice; never confirm whether a person is under investigation.
   - Every report gets a reference number; escalate weapon/injury mentions to human operator immediately.
2. `neighborhood-watch.yaml` — community watch groups. Offerings: suspicious-activity report intake, patrol
   shift signup, community meeting scheduling (capacity), incident escalation path to police non-emergency line,
   new-member onboarding. HARD RULE: never confront suspects — observe & report only.
3. `civic-services.yaml` — 311-style municipal issue reporting. Offerings: pothole report, broken streetlight
   report, waste/missed-collection report, water leak report, noise complaint, follow-up/status call.
   Each report = a booking ("inspection/assessment slot") with a ticket reference; knowledgeSeed includes
   department routing (roads/lighting/sanitation/water/parks), SLA expectations, and "what to have ready"
   (location, photo description, landmark). HARD RULE: gas leaks/down power lines → emergency number FIRST.

Registry: add 3 entries to `industries/index.json` (same sha256 flow; total becomes 30); add 2 seed tenants to
`scripts/seed-industries.sh` (`city-civic`, `community-watch`); add 3 rows to `docs/industries.md`.

## Part B — billing-engine, Rust :7012 (Agent B)

New service `services/billing-engine/`. Compile-risk discipline: NO Rust toolchain in this environment
(like payments-service/gateway-edge) — mirror payments-service's proven dependency set EXACTLY
(axum 0.7, tokio 1 full, serde/serde_json, reqwest 0.12 rustls, rdkafka 0.36 cmake-build, tracing,
uuid 1, chrono 0.4, async-trait, thiserror) plus ONLY these two well-established additions:
`sqlx = { version = "0.7", features = ["runtime-tokio-rustls","postgres","uuid","chrono","json"] }` and
`qrcode = { version = "0.14", default-features = false, features = ["svg"] }`. No other deps. 2021 edition.

### B1. Metering ingestion
- Kafka consumer group `billing-engine` on topic `opendesk.usage.events` (CloudEvents; data =
  `{tenant_id, metric, value, ts, meta}` — see services/booking-service/internal/bookingops/usage.go).
- Idempotent: `processed_events(event_id text pk, processed_at)`; skip duplicates (at-least-once source).
- Insert into `usage_records(id uuid pk, tenant_id uuid, metric text, value bigint, ts timestamptz,
  meta jsonb, event_id text unique)`.

### B2. Rating & invoicing
- `rate_cards(tenant_id uuid, metric text, unit_price_cents bigint, included_quota bigint, currency text,
  pk(tenant_id,metric))` + seed plan presets (free/standard/pro) in a migration; API to upsert a tenant's
  rate card (`PUT /v1/rate-cards/{tenant_id}`).
- Invoice generation `POST /v1/invoices/generate {tenant_id, period:"YYYY-MM"}`: aggregate usage_records for
  the period, bill `max(0, total-included_quota) * unit_price_cents` per metric line item, create
  `invoices(id uuid pk, tenant_id, period, status text check in (draft,issued,paid,void,past_due),
  subtotal_cents, currency, line_items jsonb, payment_ref text, created_at, issued_at, paid_at)`;
  regenerate = replace draft only; unique (tenant_id, period) partial index where status <> 'void'.
- `GET /v1/invoices?tenant_id=&status=`, `GET /v1/invoices/{id}`, `POST /v1/invoices/{id}/issue`,
  `POST /v1/invoices/{id}/void`. Auth: `X-Tenant-ID` header must match tenant_id (service-to-service via
  APISIX; JWT role check: realm roles `owner`/`admin` required for generate/void — read `x-user-roles`
  header injected by the gateway, deny 403 otherwise; `/webhooks/paystack` exempt).
- Ledger: double-entry receivables via the same SimLedgerClient trait pattern as payments-service
  (ledger codes 200=AR-control, 201=revenue, 202=payments-clearing); invoice issued → DR AR / CR revenue;
  paid → DR clearing / CR AR.

### B3. QR payments
- `POST /v1/invoices/{id}/payment-link` → if `PAYSTACK_SECRET_KEY` set: Paystack
  `POST https://api.paystack.co/transaction/initialize {email, amount (kobo), reference: invoice_id,
  callback_url, metadata}` → `authorization_url`; else fallback mode `static`: EMVCo-like merchant payload
  built from `BILLING_STATIC_ACCOUNT` env (merchant name/account). Store `payment_ref` on the invoice.
- `GET /v1/invoices/{id}/qr` → SVG QR (qrcode crate) encoding the payment link/payload; 404 unless
  payment_ref exists. Content-Type image/svg+xml.
- `POST /webhooks/paystack` — verify `x-paystack-signature` = HMAC-SHA512(body, PAYSTACK_SECRET_KEY)
  (constant-time compare; 401 on mismatch). On `charge.success` with matching reference → invoice paid
  (idempotent: already-paid → 200), ledger clearing entry, emit CloudEvent
  `com.opendesk.billing.InvoicePaid` to topic `opendesk.billing.events` (rdkafka producer; also used by
  notification-worker later — produce only).
- Dunning sweep: interval task (`DUNNING_INTERVAL_S`, default 3600) marking `issued` invoices older than
  `INVOICE_DUE_DAYS` (default 14) as `past_due`.

### B4. Wiring
- Migration: follow the repo's existing migration pattern (check infra/postgres/ or service migrations dirs).
- docker-compose.yml: ADD a `billing-engine` service block only (build context, :7012, env: DATABASE_URL
  per-service role, KAFKA_BROKERS, PAYSTACK_SECRET_KEY, BILLING_STATIC_ACCOUNT, DUNNING_INTERVAL_S).
- infra/apisix/apisix.yaml: ADD upstream billing-engine:7012 + route `/api/billing/*` with the file's
  standard jwt pattern (mirror an existing authenticated route); `/webhooks/paystack` goes through the
  existing public webhooks route pattern (no jwt) — extend the existing webhooks route URI list if that is
  the established pattern, else add a second public route.
- docs/billing.md: architecture, rate-card plans, QR flows (Paystack + static EMV), webhook setup,
  role matrix, dunning, API reference.
- Unit tests (cargo test, `#[cfg(test)]` with tokio::test where practical): rating math (quota, rounding),
  EMV payload CRC16, Paystack signature verify (known-vector), invoice state machine transitions.
  Since cargo is unavailable, tests are reviewed statically — keep them dependency-light.

## Part C — Role-based KPI dashboards + white label (Agent C)

### C1. Keycloak realm (infra/keycloak/realm-opendesk.json)
- ADD realm roles: `analyst` ("KPI/analytics dashboards, no mutations") and `billing`
  ("invoices, rate cards, payment links"). Keep existing owner/admin/staff/viewer untouched.

### C2. Permify schema (infra/permify/schema.perm)
- ADD `relation analyst @user` and `relation billing @user` to entity organization.
- ADD permissions: `view_analytics = owner or admin or analyst`, `view_billing = owner or billing`,
  `manage_billing = owner or billing`. Keep all existing relations/permissions byte-identical.

### C3. admin-web dashboards (apps/admin-web)
- New `app/app/[orgSlug]/analytics/` page (or extend the existing Wave-3 analytics page if present — check
  first): KPI cards (bookings this period, revenue, call minutes, avg sentiment/CSAT, channel breakdown
  whatsapp/telegram/web/voice) + trend chart using the charting approach already in the repo (no new heavy
  deps — reuse what's there; if nothing, pure SVG sparklines).
- Role gating: read the current user's Keycloak realm roles from the existing auth/session helper used
  elsewhere in admin-web (FIND it, reuse it). Nav + page sections: analytics visible to
  owner/admin/analyst; a new "Billing" section (invoice list, totals, "Generate invoice", QR code display
  via `{gateway}/api/billing/v1/invoices/{id}/qr`) visible to owner/billing only; staff/viewer never see
  either. Server-side guard (redirect) + client-side nav hiding.
- Billing section talks to the billing-engine contract in Part B (through the existing api gateway pattern;
  base path `/api/billing`).
- White label: extend the existing theme editor (Wave 3) — per-tenant branding fields (logo URL, primary
  color, company display name, custom domain note) saved via the existing site-config persistence; public
  site header/footer render the tenant brand name/logo when set. Follow existing persistence patterns.
- `npx tsc --noEmit` must pass (node available; npm ci first).

### C4. docs
- `docs/security/roles.md`: role → capability matrix (Keycloak realm role × Permify permission × APISIX
  route × dashboard section), onboarding steps for assigning roles, white-label setup notes.

## Part D — Marketing website (Agent D)

- `apps/marketing/` — dependency-free static site: `index.html` + `styles.css` (+ optional `main.js`),
  no build step. Sections: hero (AI receptionist for every business, open-source), omnichannel strip
  (voice/WhatsApp/Telegram/web/SMS), feature grid (bidirectional Twenty CRM, 30 vertical packs, billing &
  QR payments, GDPR/NDPA compliance, self-hosted middleware stack), verticals showcase (grouped: health,
  finance, public safety, education, commerce, developing-market), pricing tiers (Free self-hosted /
  Standard / Pro — consistent with Part B rate presets), CTA (GitHub repo + book demo), footer.
- Design: warm low-saturation palette, ample whitespace, no blue-purple gradients, system font stack,
  responsive, <150KB total. All copy must be factually consistent with the platform (27→30 packs,
  Go/Rust/Python/TS, the middleware list).
- README.md inside apps/marketing with local preview instructions.
