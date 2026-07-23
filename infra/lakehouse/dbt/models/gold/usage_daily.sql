-- gold.usage_daily — total usage per tenant per day per metric (Wave 5 #9).
-- Monetization foundation (STRATEGY.md §2 item 2): call-minutes, bookings,
-- messages, AI tokens per tenant feed usage-metered API pricing. Source is
-- bronze.usage_events (analytics-pipeline consumer on opendesk.usage.events).
-- Emitted events carry no event_id in v1, so dedupe is exact-row based
-- (at-least-once delivery can otherwise double-count an identical retry).
with usage as (
    select
        tenant_id,
        metric,
        value,
        cast(date_trunc('day', occurred_at) as date) as day,
        row_number() over (
            partition by tenant_id, metric, value, occurred_at, coalesce(meta, '')
            order by occurred_at
        ) as rn
    from {{ source('bronze', 'usage_events') }}
    where tenant_id is not null
      and metric is not null
      and value is not null
      and occurred_at is not null
)

select
    tenant_id,
    day as date,
    metric,
    sum(value) as total_value
from usage
where rn = 1
group by tenant_id, day, metric
