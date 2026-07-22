# infra/twenty — Twenty CRM bootstrap

One-shot scripts for the self-hosted Twenty instance (SPEC-CRM §A/§B). The
Twenty stack itself lives in `infra/docker-compose.crm.yml` (included by the
root `docker-compose.yml`); the full integration guide is
[docs/integrations/twenty-crm.md](../../docs/integrations/twenty-crm.md).

## setup-webhooks.sh — reverse-sync webhook registration

Registers the Twenty → OpenDesk webhook on the Twenty workspace,
**idempotently** (existing webhook with the same `targetUrl` is left alone):

```bash
export TWENTY_API_KEY=eyJhbGciOi...        # Twenty Settings → API & Webhooks
export TWENTY_WEBHOOK_SECRET=...           # must equal crm-sync's TWENTY_WEBHOOK_SECRET
./infra/twenty/setup-webhooks.sh
```

Defaults: `TWENTY_API_URL=http://localhost:3100`,
`WEBHOOK_TARGET_URL=http://crm-sync:7010/webhooks/twenty`,
operations `person.created,person.updated,task.updated`.

The script creates one webhook pointing at crm-sync's HMAC-verified intake
(`POST /webhooks/twenty`). crm-sync re-emits those events as CloudEvents on
`opendesk.crm.events`, where the reverse worker (consumer group
`crm-sync-reverse`) applies them to OpenDesk (person → contact upsert,
task DONE → booking CRM note). Field names follow Twenty's v1 REST webhook
object schema and are **version-sensitive** — see the comments in the script.
