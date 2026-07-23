-- 0001_init.sql — billing-engine schema (SPEC-W7 Part B).
-- Applied idempotently by the service at startup (sqlx::raw_sql over this file,
-- same bootstrap pattern as notification-worker). The `billing` database itself
-- is created by infra/postgres/init-scripts/00-create-dbs.sql on first cluster
-- boot; least-privilege role grants live in 05-app-roles.sql.
--
-- No RLS here (unlike 01-booking-schema.sql): billing is a cross-tenant
-- aggregation service; tenant isolation is enforced at the HTTP layer via the
-- X-Tenant-ID header contract (SPEC-W7 B2) and every query filters tenant_id
-- explicitly. The app role gets FORCE-free plain grants.

-- NOTE: no CREATE EXTENSION here — gen_random_uuid() is built into pg_catalog
-- since Postgres 13, and the least-privilege app_billing role may not hold
-- CREATE on the database. (01-booking-schema.sql installs pgcrypto as the
-- bootstrap superuser; billing deliberately does not need it.)

-- ---------------------------------------------------------------------------
-- B1: metering ingestion
-- ---------------------------------------------------------------------------

-- Idempotency ledger for the at-least-once usage event source.
CREATE TABLE IF NOT EXISTS processed_events (
    event_id     TEXT PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS usage_records (
    id       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    metric   TEXT NOT NULL,
    value    BIGINT NOT NULL CHECK (value >= 0),
    ts       TIMESTAMPTZ NOT NULL,
    meta     JSONB NOT NULL DEFAULT '{}'::jsonb,
    event_id TEXT NOT NULL UNIQUE REFERENCES processed_events (event_id)
);
CREATE INDEX IF NOT EXISTS idx_usage_records_tenant_ts
    ON usage_records (tenant_id, ts);
CREATE INDEX IF NOT EXISTS idx_usage_records_tenant_metric_ts
    ON usage_records (tenant_id, metric, ts);

-- ---------------------------------------------------------------------------
-- B2: rating & invoicing
-- ---------------------------------------------------------------------------

-- Plan presets seeded below; tenants get concrete rate cards copied/overridden
-- via PUT /v1/rate-cards/{tenant_id}.
CREATE TABLE IF NOT EXISTS plan_presets (
    plan              TEXT NOT NULL,
    metric            TEXT NOT NULL,
    unit_price_cents  BIGINT NOT NULL CHECK (unit_price_cents >= 0),
    included_quota    BIGINT NOT NULL DEFAULT 0 CHECK (included_quota >= 0),
    currency          TEXT NOT NULL DEFAULT 'USD',
    PRIMARY KEY (plan, metric)
);

CREATE TABLE IF NOT EXISTS rate_cards (
    tenant_id         UUID NOT NULL,
    metric            TEXT NOT NULL,
    unit_price_cents  BIGINT NOT NULL CHECK (unit_price_cents >= 0),
    included_quota    BIGINT NOT NULL DEFAULT 0 CHECK (included_quota >= 0),
    currency          TEXT NOT NULL DEFAULT 'USD',
    PRIMARY KEY (tenant_id, metric)
);

CREATE TABLE IF NOT EXISTS invoices (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL,
    period         TEXT NOT NULL, -- "YYYY-MM"
    status         TEXT NOT NULL DEFAULT 'draft'
                   CHECK (status IN ('draft','issued','paid','void','past_due')),
    subtotal_cents BIGINT NOT NULL DEFAULT 0 CHECK (subtotal_cents >= 0),
    currency       TEXT NOT NULL DEFAULT 'USD',
    line_items     JSONB NOT NULL DEFAULT '[]'::jsonb,
    payment_ref    TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    issued_at      TIMESTAMPTZ,
    paid_at        TIMESTAMPTZ
);
-- One non-void invoice per (tenant, period): regenerate replaces drafts.
CREATE UNIQUE INDEX IF NOT EXISTS uq_invoices_tenant_period_active
    ON invoices (tenant_id, period) WHERE status <> 'void';
CREATE INDEX IF NOT EXISTS idx_invoices_tenant_status
    ON invoices (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_invoices_dunning
    ON invoices (issued_at) WHERE status = 'issued';

-- ---------------------------------------------------------------------------
-- Plan presets (SPEC-W7 B2; mirrored by the marketing site's pricing tiers).
-- Metrics follow services/booking-service usage metering (metric "booking")
-- plus the platform's other metered units. Amounts in minor units (cents).
-- ---------------------------------------------------------------------------
INSERT INTO plan_presets (plan, metric, unit_price_cents, included_quota, currency) VALUES
    ('free',     'booking',      0, 100,   'USD'),
    ('free',     'call_minutes', 0, 100,   'USD'),
    ('free',     'message',      0, 1000,  'USD'),
    ('standard', 'booking',      50, 1000, 'USD'),
    ('standard', 'call_minutes', 5, 500,   'USD'),
    ('standard', 'message',      1, 10000, 'USD'),
    ('pro',      'booking',      25, 10000,'USD'),
    ('pro',      'call_minutes', 2, 5000,  'USD'),
    ('pro',      'message',      1, 100000,'USD')
ON CONFLICT (plan, metric) DO NOTHING;
