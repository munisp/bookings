-- gold.geo_service_area_coverage — passthrough view over the Iceberg table
-- written by the Spark geo_analytics job (SPEC-W8 Part B1). Bookings inside
-- vs outside each tenant service area per day (ST_Within spatial join in
-- Spark/Sedona). Same passthrough convention as reco_pricing.
{{ config(materialized='view') }}

select
    tenant_id,
    day,
    service_area_id,
    service_area_name,
    bookings_inside,
    bookings_outside,
    coverage_share
from iceberg.gold.geo_service_area_coverage
