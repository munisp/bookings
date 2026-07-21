-- gold.no_show_rate — no-shows as a share of completed bookings per tenant per day.
-- Denominator: confirmed + no_show bookings (i.e. appointments that were expected
-- to happen); NULL when nothing was expected that day.
with daily as (
    select * from {{ ref('daily_bookings_per_tenant') }}
)

select
    tenant_id,
    day,
    bookings_confirmed,
    no_shows,
    cast(no_shows as double) / nullif(bookings_confirmed + no_shows, 0) as no_show_rate
from daily
where bookings_confirmed + no_shows > 0
