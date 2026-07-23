# Industry Workflow Packs

Industry packs are YAML bundles that tailor a tenant to a vertical: vocabulary,
booking policy, the voice agent's persona, seeded catalog/knowledge, and the
Temporal workflow that runs alongside every booking. Packs live in
`industries/` at the repo root and are mounted read-only into identity-service
and notification-worker (`./industries:/industries:ro`, `INDUSTRIES_DIR=/industries`).

## Pack schema reference

Every pack is a single YAML file named `<id>.yaml` with exactly this schema:

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Pack identifier (must match the file name): `salon`, `clinic`, `consultancy`, `support-desk`, `nigeria-sme`, `banking`, `insurance`, `government`, `travel`, `ecommerce`. Validated by identity-service on `POST /v1/tenants`. |
| `displayName` | string | yes | Human-readable name shown in the admin dashboard ("Industry pack" card). |
| `terminology` | map | yes | Vocabulary merged into `tenant.terminology`. Keys: `offering`, `team_member`, `booking`, `contact`. Tenant-level overrides win over pack values. |
| `agentPersona` | string (block) | yes | Appended to the voice agent system prompt at tenant bootstrap. Keep it persona/policy text, not instructions that conflict with tool rules. |
| `bookingPolicy` | map | yes | See sub-fields below. |
| `bookingPolicy.depositPercent` | int 0–100 | yes | Deposit hold = `ceil(price_cents × pct / 100)`; passed through the saga to `HoldDeposit`. `0` disables deposits. |
| `bookingPolicy.noShowFeeCents` | int | yes | Fee charged on a no-show signal (ledger code 103). |
| `bookingPolicy.phoneConfirmation` | bool | yes | Require a confirmed contact phone before booking mutations. |
| `bookingPolicy.intakeRequired` | bool | yes | Whether an intake form must be completed before the appointment (drives `ClinicIntakeWorkflow`-style reminders). |
| `bookingPolicy.cancellationWindowHours` | int | yes | Free-cancellation window; enforced by booking-service on cancel. |
| `temporalWorkflow` | string | yes | One of `SalonDepositWorkflow`, `ClinicIntakeWorkflow`, `ConsultancyFollowupWorkflow`, `SupportEscalationWorkflow`. Started as a child of `BookingSagaWorkflow` after `ConfirmBooking`. |
| `offerings` | list | yes | Catalog seed: `{name, duration_min, buffer_min, price_cents, capacity}` per entry. Seeded idempotently by name. |
| `reminders` | map | yes | `offsets: ["24h","1h", …]`, `channels: ["email","sms", …]` — reminder schedule for the pack. |
| `knowledgeSeed` | list | yes | `{title, body}` documents seeded into knowledge-service (idempotent by title). |
| `dashboardLabels` | map | yes | `bookingSingular`, `bookingPlural`, `customerTerm` — admin-web copy; merged below tenant terminology, above built-in defaults. |
| `languages` | list | no | ISO-639 codes the deployment supports (e.g. `[en, pcm]`). Bounds the voice runtime's language auto-switch set; validated at the voice runtime's pack consumption point (`validate_pack_languages`), not by identity. |
| `consentText` | string (block) | no | Data-processing / call-recording consent notice (GDPR/NDPA). Validators tolerate it; the Go `Pack`/`Summary` structs carry it (`consentText` in identity's resolved pack summary, `omitempty`) — see docs/compliance/ndpa.md. |
| `languages` | list of strings | no | ISO-639 language codes the deployment supports (e.g. `[en, pcm]`). Optional; carried through the Go `Pack`/`Summary` structs (`languages`, `omitempty`). |
| `agents` | list | no | Multi-agent crew (SPEC-W3 §4): `{id, name, persona, intents}` — the voice runtime routes turns to a specialist persona on intent-keyword match. |

## The built-in packs

| Pack | Terminology | Booking policy | Workflow | Notable behavior |
|---|---|---|---|---|
| **salon** | stylist / appointment / client | 30% deposit, no-show fee | `SalonDepositWorkflow` | Deposit verification before the appointment; prep-note task for the stylist |
| **clinic** | practitioner / appointment / patient | intake form required | `ClinicIntakeWorkflow` | Intake reminder T-72h, consent reminder, staff alert at T-2h if incomplete; HIPAA-ish PII care note in the agent persona; no discounts |
| **consultancy** | consultant / session / client | free discovery call | `ConsultancyFollowupWorkflow` | Post-call follow-up email, proposal follow-up Task created in the CRM, invoice reminder at T-7d |
| **support-desk** | agent / ticket / requester | — | `SupportEscalationWorkflow` | 4h first-response SLA timer, escalation to the tenant owner + priority event on breach; ticket terminology |
| **nigeria-sme** | staff / appointment / customer | 30% deposit, ₦2,000 no-show fee, 12h cancellation window | `SalonDepositWorkflow` | Pidgin-first persona with register mirroring (code-switching rules); NGN pricing (kobo); OPay/PalmPay/transfer/cash payments; "no network wahala" rescheduling; `languages: [en, pcm]`; NDPA consent text (pair with `infra/privacy/ndpa-profile.env`, see docs/compliance/ndpa.md) |
| **banking** | relationship manager / appointment / customer | intake required (KYC checklist), no fees | `ClinicIntakeWorkflow` | Security-conscious receptionist: hard rules against discussing balances/transactions, fraud-hotline triage; KYC document checklist at T-72h; kyc-officer + loan-advisor crew agents |
| **insurance** | advisor / appointment / policyholder | intake required, no fees | `ClinicIntakeWorkflow` | Empathetic FNOL persona with structured incident intake (policy number, incident date, description, injuries y/n); claims-intake + policy-service crew agents |
| **government** | officer / appointment / citizen | intake required (document checklist), no fees | `ClinicIntakeWorkflow` | Formal plain-language, accessibility-first persona; hard rule against unofficial fees ("no other payments are required" in the fee-schedule seed); languages en + pcm |
| **travel** | concierge / reservation / guest | 48h cancellation window | `ConsultancyFollowupWorkflow` | Warm concierge persona; group capacities (shuttle 8, city tour 15); `check_flight_status` demo custom tool (stand-in URL until the Amadeus plugin lands) |
| **ecommerce** | associate / slot / customer | 4h cancellation window, capacity-based slots | `SupportEscalationWorkflow` | Order-support persona (verify via order lookup, never guess); Shopify + WooCommerce order-lookup custom tools (plugin hosts allowlisted via `PLUGIN_ALLOWED_HOSTS`); order-support + sales-assistant crew agents |

## How onboarding applies a pack

```
POST /v1/tenants {slug, name, ..., industry: "clinic"}
        │
        ▼
identity-service                 validates industry against pack ids in /industries,
   │                             stores tenants.industry, includes it in the
   │                             onboarding trigger payload
   ▼
TenantProvisioned event (opendesk.identity.events)
   ▼
notification-worker: TenantOnboardingWorkflow(industry)
   │  activities: keycloak/permify provisioning (existing) …
   ▼
ApplyIndustryPack activity
   ├─ reads industries/<id>.yaml
   ├─ Dapr-invoke booking-service    → seed offerings        (idempotent by name)
   ├─ Dapr-invoke knowledge-service  → seed knowledgeSeed    (idempotent by title)
   └─ POST identity /internal/tenants/{slug}/terminology
                                     → merge pack.terminology into tenant row
```

After onboarding:

* `GET /v1/tenants/{slug}` returns `industry` plus a resolved `pack` summary
  (`terminology`, `bookingPolicy`, `dashboardLabels`) so the voice runtime and
  the web dashboard consume packs without parsing YAML themselves.
* `BookingSagaWorkflow` receives the tenant's `industry`; after
  `ConfirmBooking` it starts the pack's `temporalWorkflow` as a child
  (unknown industries fall back to `SalonDepositWorkflow`).
* If `bookingPolicy.depositPercent > 0`, the saga's deposit amount is
  `ceil(price × pct)`.
* The voice runtime appends `pack.agentPersona` to the agent system prompt
  (guarded — absent packs change nothing).

## Temporal workflow variants

| Workflow | Trigger | Signals | Timers | Actions on fire |
|---|---|---|---|---|
| `SalonDepositWorkflow(bookingId, tenantSlug)` | Child of booking saga (salon / default) | `NoShow` | booking start − cancellation window | Verify deposit hold via payments activity; if inside the cancellation window with no deposit → reminder; on `NoShow` → charge no-show fee activity |
| `ClinicIntakeWorkflow(bookingId, tenantSlug)` | Child of booking saga (clinic) | `IntakeCompleted` | T-72h, T-2h before start | T-72h: intake reminder email (form link placeholder); T-2h with intake incomplete → staff alert task |
| `ConsultancyFollowupWorkflow(bookingId, tenantSlug)` | Child of booking saga (consultancy) | — | booking end, T-7d | After end: follow-up email + CRM follow-up Task via Dapr-invoke crm-sync (`POST /v1/tasks`); T-7d: proposal reminder |
| `SupportEscalationWorkflow(ticketId, tenantSlug)` | Ticket created (support-desk) | `Responded` | 4h SLA | On `Responded` → complete; on timeout → email tenant owner + emit priority-flag event to `opendesk.crm.events` |

All variants run on task queue `opendesk-main` in the notification-worker and
are registered alongside the existing saga/reminder workflows.

## Authoring a new pack

1. **Copy an existing pack** as a starting point:

   ```bash
   cp industries/salon.yaml industries/barbershop.yaml
   ```

2. **Edit the fields** — every field in the schema table above is required.
   Choose an existing `temporalWorkflow` (the id must be a workflow registered
   in notification-worker); pick the closest variant, e.g.
   `SalonDepositWorkflow` for any deposit-based vertical.

3. **Validate the YAML** parses and matches the schema:

   ```bash
   python3 -c "import yaml,sys; yaml.safe_load(open('industries/barbershop.yaml'))"
   ```

4. **Register the id.** identity-service validates `industry` against the pack
   ids found in `INDUSTRIES_DIR`; a new file is picked up on restart
   (`docker compose restart identity notification`).

5. **Seed a demo tenant** with the new pack:

   ```bash
   curl -sf -X POST http://localhost:7001/v1/tenants \
     -H 'content-type: application/json' \
     -d '{"slug":"acme-barber","name":"Acme Barbershop","industry":"barbershop",
          "timezone":"Europe/London","currency":"GBP","locale":"en-GB","plan":"pro"}' | jq .
   ```

6. **Verify:** the onboarding workflow's `ApplyIndustryPack` activity seeded
   offerings/knowledge (`GET http://localhost:7002/v1/offerings` with
   `X-Tenant-Slug: acme-barber`), and
   `GET http://localhost:7001/v1/tenants/acme-barber` shows the resolved pack
   summary. New bookings run the pack's workflow — visible in the Temporal UI
   (http://localhost:8233) as a child of `BookingSagaWorkflow`.

> A pack that needs **new runtime behavior** (not just new data) requires a new
> Temporal workflow in notification-worker plus its registration and tests —
> follow `SalonDepositWorkflow` as the template.
