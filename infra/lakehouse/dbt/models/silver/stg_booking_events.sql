-- stg_booking_events: lightly standardized view over bronze.booking_events.
-- Heavy dedupe happens in the Spark silver job; this view normalizes enums/casing
-- and pre-computes the event day used by all gold marts.
with source as (
    select * from {{ source('bronze', 'booking_events') }}
),

standardized as (
    select
        event_id,
        lower(event_type) as event_type,          -- bookingcreated|bookingconfirmed|...
        tenant_id,
        booking_id,
        lower(status) as status,
        coalesce(lower(source), 'unknown') as source,
        price_cents,
        upper(currency) as currency,
        starts_at,
        occurred_at,
        cast(date_trunc('day', occurred_at) as date) as event_day
    from source
    where event_id is not null
      and tenant_id is not null
      and booking_id is not null
)

select * from standardized
