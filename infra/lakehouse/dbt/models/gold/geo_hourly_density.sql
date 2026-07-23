-- gold.geo_hourly_density — passthrough view over the Iceberg table written
-- by the Spark geo_analytics job (SPEC-W8 Part B1). Demand heat cells by
-- hour-of-week (day_of_week 1=Sunday..7=Saturday, hour 0-23) for staffing
-- heatmaps. Cell geometry intentionally omitted; join to gold.geo_demand_h3
-- on h3_cell or regenerate with Sedona ST_H3ToGeom. Same passthrough
-- convention as reco_pricing.
{{ config(materialized='view') }}

select
    tenant_id,
    day_of_week,
    hour,
    h3_cell,
    h3_cell_str,
    bookings
from iceberg.gold.geo_hourly_density
