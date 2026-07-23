# messaging-gateway

Outbound SMS/WhatsApp gateway for the Nigeria messaging channel. Owns the
provider credentials, the retry policy and the provider error mapping for
**Termii**, **Africa's Talking** and the **WhatsApp Cloud API**, and exposes
one small REST surface that the `notification-worker` reaches through the
Dapr HTTP output bindings `bindings-termii`, `bindings-africastalking` and
`bindings-whatsapp`. See
[docs/integrations/messaging-channels.md](../../docs/integrations/messaging-channels.md)
for provider setup and channel routing.

## Endpoints

| Method | Path | Body | Upstream |
|---|---|---|---|
| GET | `/healthz` | — | liveness probe (`{"status":"ok"}`) |
| GET | `/metrics` | — | Prometheus counters `messaging_gateway_sends_total{provider,result}` |
| POST | `/v1/termii/sms` | `{to, message, sender_id?}` | Termii `POST /api/sms/send` (`{api_key, to, from, sms, type:"plain", channel:"generic"}`) |
| POST | `/v1/africastalking/sms` | `{to, message, from?}` | Africa's Talking `POST /version1/messaging` (form-encoded `username/to/message/from`, `apiKey` header) |
| POST | `/v1/whatsapp/send` | `{to, message, template?}` | WhatsApp Cloud API `POST /{phone_number_id}/messages` (free-form text; template message when `template` is set) |

Future (not implemented): `POST /v1/ussd/session` — USSD session handling
for feature-phone journeys (Termii / Africa's Talking USSD gateways).

## Behaviour

- HTTP clients use a 10s timeout; sends are retried up to **2 times** on
  5xx, 429 and transport errors (100ms/200ms backoff). Provider 4xx is
  **never retried** and is mapped to `400` with the provider body in
  `provider_body`. Persistent 5xx/transport failures map to `502`.
- Structured logs (zap) record provider, result, attempts, provider status
  and duration — **never the message body** (PII).
- A provider whose credentials are missing answers `503`.

## Configuration

| Env var | Default | Description |
|---|---|---|
| `PORT` | `7011` | HTTP listen port |
| `TERMII_API_KEY` | — | Termii API key (dashboard → API) |
| `TERMII_SENDER_ID` | `OpenDesk` | Default Termii sender id (registered sender id) |
| `TERMII_BASE_URL` | `https://v2.api.termii.com` | Override (tests / mock) |
| `AT_API_KEY` | — | Africa's Talking API key |
| `AT_USERNAME` | — | Africa's Talking app username (`sandbox` on the sandbox) |
| `AT_BASE_URL` | `https://api.africastalking.com` | Override; sandbox: `https://api.sandbox.africastalking.com` |
| `AT_FROM` | — | Default sender id / shortcode (optional) |
| `WHATSAPP_TOKEN` | — | WhatsApp Cloud API access token |
| `WHATSAPP_PHONE_NUMBER_ID` | — | WhatsApp Business phone number id |
| `WHATSAPP_BASE_URL` | `https://graph.facebook.com/v21.0` | Override (tests / mock) |

## Development

```sh
go build ./... && go vet ./... && go test ./...
docker build -t opendesk/messaging-gateway .
```
