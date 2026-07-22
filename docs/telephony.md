# Telephony — SIP inbound (Wave 5 #1)

Inbound PSTN calls ring the AI receptionist through LiveKit SIP: the carrier
hands calls to a LiveKit **inbound trunk**, a **dispatch rule** creates one
room per dialed number (`call-{number}`), and the voice worker
(`services/voice-agent-runtime`) bootstraps the session from the SIP
participant instead of the web `site-{slug}` room convention.

```
PSTN caller ── carrier SBC ──> LiveKit SIP inbound trunk (numbers list)
                                   │ dispatch rule (callee, roomPrefix call-)
                                   ▼
                          room `call-+15551234567`
                                   │ app/sip.py bootstrap
                                   ▼
              tenant from TENANT_PHONE_MAP[dialed] → receptionist flow
              caller ID → session.confirmed_phone (carrier-asserted)
```

## Carrier checklist

What to order/verify with the carrier (see
[runbooks/capacity-planning.md](runbooks/capacity-planning.md) §4 for the
full capacity math — telephony is the one plane you **buy**, not deploy):

- **Channels (concurrent calls)** — hard cap, 1 SIP channel per active PSTN
  call. Order for peak concurrency + headroom; procurement lead time is
  *weeks*, so size ahead of campaigns.
- **CPS (calls per second, start rate)** — the carrier's limit on NEW call
  attempts per second. Inbound bursts (advertising, opening hours) can trip
  it just like outbound; ask for the CPS figure in writing.
- **Numbers** — E.164 DIDs per tenant (local presence) or shared numbers
  with tenant routing (see mapping below).
- **Signalling** — SIP over UDP/TCP 5060 or TLS 5061 to the LiveKit server's
  SIP ingress; restrict to the carrier's SBC IPs (`allowed_addresses` on the
  trunk) and enable digest auth when offered.
- **Codecs/media** — G.711 μ-law/A-law is the safe baseline; confirm RTP port
  range and NAT handling with the LiveKit deployment.
- **Compliance** — caller-ID passthrough policy, call-recording consent
  rules per jurisdiction, emergency-call disclaimer (the receptionist is not
  an emergency service).

## Provisioning

```bash
# 1. edit numbers + hardening, then create trunk + dispatch rule
deploy/livekit-sip/setup.sh           # uses lk (livekit-cli)
LK_URL=https://lk.example.com LK_KEY=... LK_SECRET=... deploy/livekit-sip/setup.sh

# 2. map numbers to tenants on the voice worker (dev-mode static map)
TENANT_PHONE_MAP='{"+15551234567":"acme","+15557654321":"glow"}'
# optional fallback for unmapped numbers (empty = reject with error log):
SIP_DEFAULT_SITE=front-desk

# 3. call the number — the room `call-+1555…` is dispatched to the agent
```

Files:

- `deploy/livekit-sip/trunk-config.example.yaml` — `SIPInboundTrunkInfo`
  (`lk sip inbound create`): name, `numbers` list, optional
  `allowed_addresses` / digest auth / krisp.
- `deploy/livekit-sip/dispatch-rule.yaml` — `CreateSIPDispatchRuleRequest`
  (`lk sip dispatch create`): `dispatchRuleCallee` with `roomPrefix: call-`
  and `randomize: false`, so rooms are deterministically `call-{dialed}`.
- `deploy/livekit-sip/setup.sh` — applies both via livekit-cli
  (`LK_URL`/`LK_KEY`/`LK_SECRET` env, dev defaults match the compose stack).

## Number → tenant mapping design

The dialed number (which of *our* numbers the customer called) selects the
tenant — the server, never the model (`app/sip.resolve_tenant`).

- **Dev mode (shipped)**: `TENANT_PHONE_MAP` JSON
  `{"+1555…": "tenant-slug"}` parsed at worker boot into
  `Settings.tenant_phone_map`; invalid entries drop out with warnings.
- **Production design**: a `phone_numbers` table owned by booking-service
  (`number E164 PK, tenant_id, site_slug, provisioned_at, carrier_ref`),
  managed by a provisioning API (`POST /v1/phone-numbers {number, tenant}`)
  and cached in the voice runtime with a short TTL. The env map mirrors the
  table lookup semantics 1:1, so swapping the source is a one-function
  change in `app/sip.py`.
- **Caller identity**: the SIP caller ID (`From` / `P-Asserted-Identity`,
  surfaced by LiveKit as the participant's `sip.phoneNumber` attribute) is
  carrier-asserted, so the session starts with `confirmed_phone` already set
  — the two-step read-back confirmation is bypassed **for SIP sessions
  only** (policy rationale in `app/sip.py` and `app/session_state.py`).
  Anonymous/withheld caller IDs get no bypass; the normal confirmation
  applies.

## Outbound calls (note)

Outbound campaigns (waitlist backfill, no-show rebook) use the same trunk
plane in the other direction. Carrier **CPS is the binding constraint** — the
existing `OutboundPacer` (`OUTBOUND_CPS`, default 1/s, see
runbooks/capacity-planning.md §4) paces call starts to stay under the
carrier's start-rate limit; channels then cap concurrency as usual.

## Failure modes

| Symptom | Likely cause | Check |
|---|---|---|
| Call rings out, no room | dispatch rule/trunk ids mismatch | `lk sip dispatch list`, trunk numbers list |
| Room created, agent errors | dialed number not in `TENANT_PHONE_MAP` and no `SIP_DEFAULT_SITE` | worker log `sip tenant resolution failed` |
| Agent asks caller to confirm phone | caller ID withheld/anonymous | expected: no carrier assertion → two-step flow |
| Calls rejected at peak | carrier channel cap or CPS | carrier portal; capacity-planning.md §4 |
