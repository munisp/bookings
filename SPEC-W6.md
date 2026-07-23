# SPEC-W6 — Omnichannel Inbound (WhatsApp + Telegram) & Developing-Country Vertical Packs

Wave 6 contract. All agents implement faithfully; no unilateral interface changes.
Working repo: `/mnt/agents/output/opendesk` (flaky FUSE mount — work in `/tmp`, rsync back additively, md5-verify).

## Part A — Omnichannel inbound (messaging-gateway, Go :7011)

### A1. Inbound webhooks (new file `internal/httpapi/webhooks.go` + `internal/channel/`)

- `GET  /webhooks/whatsapp` — Meta verification: params `hub.mode`, `hub.verify_token`, `hub.challenge`.
  If `hub.mode=subscribe` and token matches `WHATSAPP_VERIFY_TOKEN` → 200 with raw challenge body; else 403.
- `POST /webhooks/whatsapp` — Meta Cloud API payload: `entry[].changes[].value`.
  - For `messages[]`: extract `{from (E.164), id, timestamp, text.body}`. Ignore non-text types (log + 200).
  - Ignore `statuses[]` (delivery receipts) silently.
  - Always answer 200 fast (Meta retries on non-200); process synchronously but bounded (25s ctx).
- `POST /webhooks/telegram` — Bot API Update JSON: `message.{message_id, chat.id, from, text}`.
  - Optional shared-secret check: header `X-Telegram-Bot-Api-Secret-Token` must equal `TELEGRAM_WEBHOOK_SECRET` when set; else 403.
  - Ignore updates without `message.text` (200).

### A2. Normalized inbound envelope (contract — exact shape)

```go
type InboundMessage struct {
    Channel   string `json:"channel"`    // "whatsapp" | "telegram"
    From      string `json:"from"`       // whatsapp: E.164 phone; telegram: chat_id as string
    MessageID string `json:"message_id"` // provider message id (idempotency)
    Text      string `json:"text"`
    Timestamp int64  `json:"timestamp"`  // unix seconds (telegram: message.date)
}
```

### A3. Channel bridge (`internal/channel/bridge.go`)

Flow per inbound message:
1. Resolve tenant via `CHANNEL_SITE_MAP` env (JSON):
   `{"whatsapp:<phone_number_id>": {"site_slug":"...","tenant_id":"<uuid>"}, "telegram:<bot_username>": {...}}`.
   WhatsApp key uses `value.metadata.phone_number_id`; Telegram key uses `TELEGRAM_BOT_USERNAME` (single bot per gateway deployment). Unmapped → log + 200 (drop).
2. Resolve-or-create conversation: `GET {conv}/v1/conversations?tenant=&contact=<from>` via Dapr invoke
   (`http://localhost:{DAPR_HTTP_PORT=3500}/v1.0/invoke/conversation-service/method/...`);
   create with `POST /v1/conversations {tenant_id, site_slug, channel, contact_phone}` if none open.
   Session/continuity key = conversation UUID (stable across restarts, durable history).
3. Record user turn: `POST /v1/conversations/{id}/turns {role:"user", text}` with header
   `X-Tenant-ID: <tenant_id>` and `Idempotency-Key: <channel>:<message_id>` (service already dedupes).
4. Agent reply: `POST {voice}/voice/chat {site_slug, message, conversation_id}` (buffered, NOT stream).
   Pass new optional field `channel` (additive to ChatRequest, default `"web"`).
5. Record assistant turn (same turns endpoint, `role:"assistant"`, idempotency key `<channel>:<message_id>:reply`).
6. Send reply via same-channel provider: whatsapp→`WhatsApp.SendMessage(to=from)`, telegram→new `Telegram.SendMessage(chat_id=from)`.
7. Failure of steps 3-6 → log + 200 (never 5xx to the webhook provider; they retry storms).

Env: `CONVERSATION_URL` / `VOICE_RUNTIME_URL` direct-base overrides (tests, no-Dapr dev);
default Dapr sidecar `http://127.0.0.1:{DAPR_HTTP_PORT}` invoke.

### A4. Telegram provider (`internal/provider/telegram.go`)

- Bot API: `POST {TELEGRAM_BASE_URL=api.telegram.org}/bot{TELEGRAM_BOT_TOKEN}/sendMessage`
  body `{chat_id, text, parse_mode:""}` (plain text only).
- Same provider semantics as existing: 10s timeout, retry once on 5xx/transport, `provider.Error` on 4xx,
  PII-safe logs (no message bodies), `Configured()` gate on token.
- New endpoint `POST /v1/telegram/send {to (chat_id string), message}` on the gateway (parity with other providers).

### A5. Voice runtime additive change (voice-agent-runtime)

- `ChatRequest`: add `channel: str = "web"` (optional, additive only). Thread into session metadata/logging.
  No behavioral change for existing callers. Keep livekit pins untouched.

### A6. Dapr + compose + docs wiring

- `infra/dapr/components/bindings.whatsapp.yaml`: add note; webhooks terminate on the gateway directly
  (providers can't call Dapr bindings inbound through APISIX without public route) — add APISIX route:
  `infra/apisix/apisix.yaml`: `/webhooks/*` → messaging-gateway:7011, WAF plugin enabled, no auth (signature/verify-token based).
- `infra/compose/docker-compose.yml`: messaging-gateway env additions (verify token, telegram token, CHANNEL_SITE_MAP).
- `docs/integrations/whatsapp-telegram.md`: full runbook (Meta app setup, verify token, webhook URL,
  Telegram BotFather + setWebhook, CHANNEL_SITE_MAP format, troubleshooting).
- Tests: `internal/httpapi/webhooks_test.go` (verify token ok/bad, whatsapp text ingestion → bridge called with
  normalized envelope, statuses ignored), `internal/channel/bridge_test.go` (full flow with httptest fakes for
  conversation + voice + provider; idempotency keys asserted), `internal/provider/telegram_test.go`.
- `go build ./... && go vet ./... && go test ./...` must pass (Go at `$HOME/sdk/go/bin/go`, `GOPROXY=https://goproxy.cn,direct`).

## Part B — 10 developing-country vertical packs (industries/*.yaml)

Follow the exact existing pack schema (see `industries/nigeria-sme.yaml` + `scripts/validate_pack.py`).
Hard rules: banking/stock-brokerage style "never do X" rules where money advice is involved; NGN/kobo pricing
where Nigerian; `languages` and `consentText` where relevant; phone-confirmation booking policy.

1. `microfinance.yaml` — Microfinance banks & SACCOs/cooperatives (esusu/ajo/chama). Offerings: savings
   contribution collection, loan application intake, field-agent visit booking, group meeting scheduling.
   Hard rule: never quote interest rates or approve loans — intake + appointment only.
2. `pharmacy.yaml` — Pharmacies & patent medicine vendors (PPMV). Refill reminders, prescription-dropoff
   pickup booking, pharmacist consult slots. Hard rule: never give dosage/medical advice; escalate to pharmacist.
3. `agri-input.yaml` — Agri-input dealers & NGO extension programs. Seasonal input ordering campaigns,
   training/demo group bookings, agronomist call-back slots. Multi-language (en/pcm/ha/yo/ig seeds).
4. `religious.yaml` — Churches/mosques & faith institutions. Service/program scheduling (500+ capacity),
   counseling/prayer appointments, dues/tithe contribution ledger inquiries (TigerBeetle-style wording),
   event hall booking. Multi-campus terminology.
5. `logistics.yaml` — Logistics & dispatch riders. COD (cash-on-delivery) confirmation calls, pickup/delivery
   slot booking, rider KYC onboarding intake, failed-delivery rescheduling.
6. `legal-aid.yaml` — Legal aid & paralegal services. Case intake (matter type screening), document checklist
   guidance, consultation booking, referral escalation to partner lawyers/Legal Aid Council.
   Hard rule: never give legal advice — intake and scheduling only.
7. `utilities-payg.yaml` — Utilities & PAYG solar. Technician installation/repair dispatch slots, payment-plan
   enrollment intake, outage/fault triage, meter/token support. Hard rule: no billing adjustments by agent.
8. `recruitment.yaml` — Recruitment & domestic staffing agencies. Candidate intake + vetting slots, employer
   consultation booking, interview scheduling, document verification appointments.
9. `isp-installer.yaml` — ISPs & cable/TV installers. Installation slot booking, fault triage + technician
   dispatch, relocation appointments, support SLA tiers in knowledgeSeed.
10. `vocational.yaml` — Vocational training & exam prep (JAMB/WAEC). Class/cohort enrollment (capacity-capped),
    trial-class booking, payment-plan intake, mock-exam scheduling.

Registry + tooling:
- Update `industries/index.json` (id, file, sha256, displayName) for all 10.
- Add 3 new seed tenants to `scripts/seed-industries.sh` (e.g. microfinance, pharmacy, logistics).
- Add rows to `docs/industries.md` table.
- `python3 scripts/validate_pack.py industries/<pack>.yaml` must pass for all 10.

## Part C — Admin channel enablement UI (apps/admin-web, TypeScript)

- New dashboard section "Channels" (settings area): per-tenant toggles WhatsApp / Telegram / Web chat with
  config fields (phone_number_id, bot username), showing the generated `CHANNEL_SITE_MAP` snippet to paste
  into the gateway env + webhook URLs to paste into Meta/Telegram consoles.
- Read-only display of channel health: `GET {gateway}/healthz` reachability badge (client-side fetch, graceful fail).
- Follow existing admin-web patterns (React + Tailwind + shadcn/ui); `npx tsc --noEmit` must pass.
