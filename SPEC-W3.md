# SPEC-W3.md — Wave 3-4: Roadmap Implementation Contract

Additive to SPEC.md + SPEC-CRM.md. Repo: /mnt/agents/output/opendesk (edit in place, NO git, /mnt is flaky — re-read after writes). Implements docs/ROADMAP.md. Open-source-first rule: no proprietary SaaS dependencies; AI models default to open weights.

## 0. Open-model-first AI stack (global change)
- Default LLM: `qwen3:8b` via Ollama. Update: root compose voice env LLM_MODEL default, ollama-init pull list (`qwen3:8b`), voice-agent-runtime config defaults + README.
- MiniMax path: documented alt — `LLM_BASE_URL=https://api.minimax.io/v1` (or local MiniMax-M2 via vLLM/Ollama where available) + `LLM_MODEL=MiniMax-M2` + `LLM_API_KEY` env. Add a model-routing table in voice README (qwen3:8b default / qwen3:32b quality / MiniMax-M2 long-context).
- Any LLM-judge / text-to-SQL / NER features below MUST use the same OpenAI-compatible env config (works with Ollama out of box).

## 1. Platform infra (Agent A)
- **CI** `.github/workflows/ci.yml`: yaml lint, go build/vet/test (4 go services), cargo check (2 rust, default features), py_compile+pytest (python), web tsc+build, `docker compose config -q`, smoke script shellcheck. 
- **E2E** `tests/e2e/` pytest suite: full flow provision→seed→availability→public book→saga events→crm sync_map→opensearch doc→lakehouse bronze row; docker-compose based fixtures; README.
- **Observability** `infra/docker-compose.observability.yml`: prometheus:9090 (scrape ALL service /metrics + apisix), grafana:3002 (provisioned datasources + 3 dashboards: platform overview, temporal/saga, ai-voice), otel-collector:4318 (receivers otlp, exporters logging+prometheus), loki:3110 + promtail (docker socket or varlog). APISIX prometheus plugin port noted. Root compose include. Grafana dashboard JSONs real (panels for kafka lag, saga latency histograms, LLM call latency, http rates).
- **Backups** `infra/backups/`: backup.sh (pg_dump all DBs, minio mc mirror, tigerbeetle data file copy, restic-style rotation to ./backups volume) + ofelia or cron sidecar in observability profile; restore.sh; runbook update.
- **HA topology** docs/ADRs/0008-production-topology.md: kafka rf=3, Patroni, TB 3-node, temporal multi-node, permify/keycloak HA, sizing table.
- **Edge appliance** `deploy/k3s/`: kustomize base (namespace opendesk, core services as Deployments referencing compose images, local-path provisioner note), kind config, MirrorMaker2 store-forward yaml, README.
- **Fluvio deploy** `infra/fluvio/deploy.sh`: smdk build+load pii-redact, connector yaml applying it to opendesk.transcripts-raw → kafka sink topic opendesk.conversation.transcripts; update fluvio README honestly.

## 2. Security & data (Agent B)
- **RLS**: booking/conversation/knowledge stores SET LOCAL app.tenant_id per tx (Go: booking store; Python services already do — verify); per-service DB roles in init scripts (app_booking/app_conversation/app_knowledge NOINHERIT + grants + FORCE RLS already on); compose PG_DSN per-service user; doc in runbook.
- **Secrets**: `.env.example` at root (all secrets, dev defaults); compose reads ${} from .env; docs/runbooks/secrets.md (SOPS/Vault prod pattern, rotation procedures).
- **Gateway authz**: /voice/* gains openid-connect bearer_only EXCEPT public chat/session used by anonymous visitors — instead: split routes: /voice/chat + /voice/session stay public but get stricter limit-count (30/min) + turnstile-note; /ws keeps in-app JWT (document why). APISIX per-plan rate limit: limit-count keyed on http_x_tenant_plan header with plan map documented.
- **OpenAppSec**: runbook section detect→prevent (learning period, enforce switch), plus openappsec API-discovery schema upload from docs/api/openapi.
- **ZAP** `scripts/security-scan.sh`: OWASP ZAP docker baseline against :9080, report to reports/, CI-adjacent.
- **GDPR** (innovation 13): Temporal `GdprExportWorkflow` + `GdprEraseWorkflow` in notification-worker; export: gather bookings/conversations/knowledge/ledger/CRM(by email/phone) → JSON bundle → MinIO bucket `exports` (add bucket init) → signed URL note (dev: path); erase: publish tombstone CloudEvents to NEW topic `opendesk.privacy.events` (add to create-topics.sh); consumers: booking (anonymize contact), conversation (delete turns), knowledge (delete docs by contact ref — skip if n/a), crm-sync (delete Twenty person via API). Endpoints: booking POST /v1/privacy/export + /v1/privacy/erase (manage_bookings permify). Topic + dapr scope additions.

## 3. Core services (Agent C)
- **Availability cache** (booking-service): go-redis v9 client, slot computation cached key `avail:{tenant}:{offering}:{member}:{day}` TTL 120s, invalidation: on booking create/reschedule/cancel delete affected keys (both REST + consumer paths); CACHE_TTL env; tests.
- **SSE streaming chat** (voice-agent-runtime): POST /voice/chat gains `stream:true` → StreamingResponse text/event-stream yielding LLM deltas through the same tool layer (non-stream path unchanged); web chat-widget updated by Agent E (coordinate: event format `data: {"delta": "..."}` / `data: {"done": true}`).
- **Turn idempotency** (conversation-service): turns accept Idempotency-Key header; unique partial index migration note + in-code dedupe returning existing turn (follow booking pattern).
- **Waitlist backfill** (innovation 7): booking: `waitlist` table (id,tenant_id,offering_id,contact_id,window_start,window_end,status,created_at) + POST/GET /v1/waitlist; notification-worker `WaitlistBackfillWorkflow` triggered by new booking event consumer signal path: on BookingCancelled → query waitlist (Dapr invoke booking GET /v1/waitlist?offering_id&status=waiting) → notify top 3 via email/sms bindings with claim token → first POST /v1/waitlist/{id}/claim (booking, transactional slot re-check) wins; others notified full.
- **Revenue intelligence** (innovation 9): `infra/lakehouse/spark/jobs/revenue_intelligence.py`: from silver bookings+payments → gold.reco_pricing (tenant_id, offering_id, peak_hour_multiplier, suggested_deposit_pct, no_show_risk_band, generated_at); dbt model gold/reco_pricing.sql passthrough+tests; analytics-pipeline GET /v1/recommendations?tenant= reading latest via pyiceberg/Trino HTTP; documented human-approval-only.
- **Text-to-SQL analytics** (innovation 8): knowledge-service POST /v1/analytics/query {tenant, question}: LLM (OpenAI-compatible env) generates Trino SQL; guardrails: AST-lite validation (single SELECT, allowlist gold.* tables, LIMIT enforced, no DDL/DML, tenant filter injected), execute via Trino HTTP :8088, return {sql, columns, rows, explanation}; timeout 20s; unit tests with fake LLM.
- **Digital twin** (innovation 12): identity POST /internal/tenants/{slug}/twin → creates tenant `{slug}-twin-{rand}` (industry copied, synthetic seed via existing onboarding, `plan='twin'`, metadata `twin_of`); Temporal `TwinCleanupWorkflow` deletes after 24h (DELETE tenant endpoint added to identity, cascade note); documented.

## 4. Voice & AI (Agent D)
- **Model defaults** (§0 above) + model routing table docs.
- **Warm handoff** (innovation 1): voice runtime tool `request_human` → creates LiveKit room `escalation-{conv}`, publishes `EscalationRequested` to opendesk.conversation.events + WS notify (edge), generates staff join token; dashboard escalation banner handled by Agent E; whisper-copilot mode: agent stays, posts suggested replies into room data channel (real LiveKit API calls, graceful when server absent).
- **Call intelligence** (innovation 3): conversation-service per-turn enrichment: lexicon sentiment (real small lexicon module, no dep) + optional LLM NER (env INTEL_LLM=off default off for speed; on → ollama qwen3:8b JSON extraction); enriched fields stored + published to `opendesk.conversation.enriched` (new topic); edge fans out /ws/intel channel; unit tests.
- **Self-improving KB** (innovation 4): knowledge-service: `kb_suggestions` table; when /v1/search top RRF score < SUGGEST_THRESHOLD env (0.35) and q looks like a question → insert suggestion; GET /v1/suggestions?tenant=, POST /v1/suggestions/{id}/approve (creates real document via existing ingest), DELETE reject. Tests.
- **Eval harness** (innovation 5): `services/voice-agent-runtime/eval/` — eval.py replays N conversations from OpenSearch `conversations` (or synthetic scenarios in eval/scenarios/*.yaml) against /voice/chat, LLM-as-judge scoring (correctness, tool-call accuracy, persona adherence) via OpenAI-compatible env, writes evals index + eval/report.md; make target `make eval`; README.
- **Multi-agent crews** (innovation 6): packs gain optional `agents: [{id, name, persona, intents:[...]}]` (add to salon/clinic yaml examples); voice runtime routes by intent match before main persona (prompt-swap per turn, session state tracks active agent); loader validation updated in identity packs.go (allow optional field, passthrough).
- **Plugin tools** (innovation 15 MVP): packs gain optional `customTools: [{name, description, method, url, bodyTemplate}]` — declarative HTTP tools; voice runtime registers them in the tool layer executing via httpx with template substitution (WASM sandbox documented as phase-2 in docs/plugins.md); validation in pack loader.
- **Voice biometrics scaffold** (innovation 2): voice-agent-runtime/app/voiceprint.py — enrollment/verify interface, resemblyzer optional-import impl, consent gate env VOICEPRINTS=off; documented honestly as scaffold + API in README.

## 5. Web (Agent E)
- Theme editor (I18): public-site page — edit theme jsonb (colors, logo URL, hero text, template) via existing PUT /v1/site; live preview pane.
- Staff self-service (I19): booking GET /v1/bookings?mine=true (JWT sub → team_members.email lookup — coordinate Agent C adds this endpoint); web "My schedule" page for staff role.
- Embed widget (I20): apps/admin-web `/embed/[siteSlug]` chromeless booking+chat page; `public/embed.js` (iframe loader snippet); docs/embedding.md; settings page shows copy-paste snippet.
- KB review queue: knowledge page adds suggestions tab (GET/approve/reject endpoints from Agent D).
- Billing: recommendations card from analytics GET /v1/recommendations (Agent C); approve-dismissal is client-side note only.
- Analytics chat: new page "Analytics" with question box → knowledge POST /v1/analytics/query → result table + generated SQL collapsible.
- Escalation banner: WS listener for EscalationRequested → toast with staff join link (Agent D provides event shape).
- Consumer lag + metrics: overview page mini-panels from prometheus? NO — keep simple: link to Grafana :3002 in nav.

## 6. Rules
- Go: build/vet/test green. Rust: only cargo check if toolchain available, else careful review. Python: py_compile + pytest where tests exist. Web: tsc clean.
- No fake placeholders in critical paths; scaffolds must be labeled honestly in READMEs.
- Update docs/ROADMAP.md checkmarks + README feature list at the end (Agent E owns README/ROADMAP updates).
