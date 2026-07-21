-- gold.revenue_daily — recognized revenue per tenant per day per currency.
-- Based on TigerBeetle transfer codes (SPEC §9): 101 capture = revenue,
-- 102 refund (negative), 103 no-show fee. Event-level dedupe on event_id (the
-- Spark silver job only covers booking_events/transcripts, so dedupe inline here).
with payments as (
    select
        tenant_id,
        booking_id,
        transfer_code,
        amount_cents,
        upper(currency) as currency,
        occurred_at,
        cast(date_trunc('day', occurred_at) as date) as day,
        row_number() over (partition by event_id order by occurred_at desc) as rn
    from {{ source('bronze', 'payment_events') }}
    where event_id is not null
      and tenant_id is not null
      and transfer_code in (101, 102, 103)   -- captured revenue, refunds, no-show fees
)

select
    tenant_id,
    day,
    currency,
    sum(amount_cents) filter (where transfer_code = 101) as captured_revenue_cents,
    sum(amount_cents) filter (where transfer_code = 102) as refunded_cents,
    sum(amount_cents) filter (where transfer_code = 103) as no_show_fees_cents,
    coalesce(sum(amount_cents) filter (where transfer_code = 101), 0)
      - coalesce(sum(amount_cents) filter (where transfer_code = 102), 0)
      + coalesce(sum(amount_cents) filter (where transfer_code = 103), 0) as net_revenue_cents
from payments
where rn = 1
group by tenant_id, day, currency
