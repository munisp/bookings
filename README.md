# OpenDesk — Open-Source AI Receptionist Platform (SOTA)

A fully open-source, multi-tenant **AI receptionist / front-desk SaaS**: every appointment-based business gets a branded public booking page with **voice + chat AI** that answers questions, checks real-time availability, and books appointments — plus an admin dashboard with bookings, calendar, analytics, payments, and CRM.

**Stack:** Go microservices · Next.js 14 · PostgreSQL · Redis · LiveKit · LiveKit Agents (Python) · Stripe · Docker Compose

---

## Quick Start

```bash
cp .env.example .env          # fill in your keys
docker compose up --build -d  # start everything
```

Then:

- **Business portal (admin):** http://localhost:3000 → sign up → create org → get slug
- **Public booking page:** http://localhost:3001/acme-dental (your org slug)
- **Voice/chat AI:** built into the booking page (requires LiveKit + OpenAI keys)
- **Monitoring:** Jaeger http://localhost:16686 · Prometheus http://localhost:9090

To seed demo data (org, services, staff, availability):

```bash
make seed
```

---

## What's Inside

| Area | Path | Tech |
|---|---|---|
| Public booking widget | `apps/booking-widget` | Next.js 14, LiveKit voice/chat |
| Admin dashboard | `apps/admin-web` | Next.js 14, NextAuth, org-scoped |
| Auth service | `services/auth-service` | Go, magic links, JWT |
| Booking service | `services/booking-service` | Go, availability engine, bookings |
| Billing service | `services/billing-service` | Go, Stripe subscriptions, webhooks |
| AI agent | `services/agent-service` | Python, LiveKit Agents, OpenAI |
| Proto contracts | `proto` | Protobuf / gRPC + gRPC-Gateway |
| Database migrations | `db/migrations` | golang-migrate |
| Infra | `docker-compose.yml`, `Makefile`, `monitoring/` | Docker, Prometheus, Jaeger |

---

## Feature Coverage

- ✅ Public booking page per org (`/{org-slug}`) with brand color + services
- ✅ AI chat (text) on booking page — knowledge-grounded via org FAQs
- ✅ AI voice on booking page (LiveKit) — *"Book me a haircut tomorrow at 2pm"*
- ✅ Real-time availability engine (staff schedules + slot locking)
- ✅ Booking flow: service → staff → slot → confirm → email
- ✅ Admin: bookings list, calendar, analytics (bookings/revenue/no-shows), CRM (customers, notes)
- ✅ Stripe billing: subscription tiers, webhooks
- ✅ Notifications: email via Resend, SMS via Twilio (adapters; set keys to enable)
- ✅ Multi-tenant org isolation (all data scoped by `org_id`)
- ✅ Observability: OpenTelemetry traces, Prometheus metrics, health endpoints

---

## Setup Details

### 1. Environment

Copy `.env.example` → `.env`. Minimum to boot everything: Postgres/Redis are in compose. For AI voice you need:

```env
OPENAI_API_KEY=sk-...
LIVEKIT_URL=wss://your-project.livekit.cloud
LIVEKIT_API_KEY=...
LIVEKIT_API_SECRET=...
```

Free-tier friendly: LiveKit Cloud, Resend, Stripe test mode all have free tiers.

### 2. Services

`make up` → runs migrations, then all services. Or `make dev` for local Go/Pnpm dev.

| Service | Port | Health |
|---|---|---|
| admin-web | 3000 | /api/health |
| booking-widget | 3001 | / |
| auth-service | 8080 | /health |
| booking-service | 8081 | /health |
| billing-service | 8082 | /health |
| agent-service | 8083 | /health (joins LiveKit rooms) |

### 3. Voice AI (LiveKit Agents)

`services/agent-service` is a Python LiveKit Agent worker. It:
- joins rooms named `booking-{orgId}-{visitorId}`,
- uses OpenAI Realtime (or STT→LLM→TTS fallback) for speech,
- has tool functions: `get_services`, `get_faqs`, `check_availability`, `create_booking` — it calls the booking-service gRPC/HTTP API to actually book.

The widget's chat/voice panel connects via LiveKit; text chat works without LiveKit (direct LLM call via API route).

### 4. Auth

- **Business owners:** email magic link → NextAuth session in admin-web.
- **Public visitors:** no account needed to book (name/email/phone collected at checkout).

---

## API / Contracts

Protos in `proto/`. Each Go service exposes:
- gRPC (internal, `:90xx`)
- HTTP via gRPC-Gateway (external, `:808x`)

Example (booking):

```
GET  /v1/orgs/{orgSlug}/services
GET  /v1/orgs/{orgId}/availability?serviceId=...&staffId=...&date=...
POST /v1/bookings { orgId, serviceId, staffId, slot, customer }
```

---

## Development

```bash
make proto     # regenerate protobuf stubs
make test      # go test ./... + pnpm test
make seed      # demo data
make logs      # tail all services
```

Repo layout is a monorepo: `apps/` (Next.js), `services/` (Go + Python), `packages/` (shared TS), `db/`, `infra/`.

---

## Roadmap / Extending

- Calendar sync (Google Calendar) — stub in booking-service
- Payments for bookings (Stripe Checkout per booking, not just subscriptions)
- More channels: WhatsApp, phone (Twilio SIP → LiveKit)
- Multi-language AI agent

---

MIT licensed. Built to be hacked on.
