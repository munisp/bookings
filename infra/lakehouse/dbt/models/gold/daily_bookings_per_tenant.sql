-- gold.daily_bookings_per_tenant — booking funnel counts per tenant per day.
with events as (
    select * from {{ ref('stg_booking_events') }}
)

select
    tenant_id,
    event_day as day,
    count(distinct booking_id) filter (where event_type = 'bookingcreated')    as bookings_created,
    count(distinct booking_id) filter (where event_type = 'bookingconfirmed')  as bookings_confirmed,
    count(distinct booking_id) filter (where event_type = 'bookingrescheduled') as bookings_rescheduled,
    count(distinct booking_id) filter (where event_type = 'bookingcancelled')  as bookings_cancelled,
    count(distinct booking_id) filter (where event_type = 'bookingnoshow')     as no_shows,
    count(distinct booking_id) filter (where source = 'voice' and event_type = 'bookingcreated') as bookings_created_voice,
    count(distinct booking_id) filter (where source = 'web'   and event_type = 'bookingcreated') as bookings_created_web,
    count(distinct booking_id) filter (where source = 'chat'  and event_type = 'bookingcreated') as bookings_created_chat
from events
group by tenant_id, event_day
