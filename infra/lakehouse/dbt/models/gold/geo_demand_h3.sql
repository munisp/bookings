-- gold.geo_demand_h3 — passthrough view over the Iceberg table written by the
-- Spark geo_analytics job (infra/lakehouse/spark/jobs/geo_analytics.py,
-- SPEC-W8 Part B1). One row per tenant × H3 res-8 cell × day, with the cell
-- polygon as WKT (Trino: ST_GeometryFromText(cell_wkt)). Same passthrough
-- convention as reco_pricing: keeps dbt docs/lineage and schema tests in one
-- place while Spark owns the writes.
{{ config(materialized='view') }}

select
    tenant_id,
    day,
    h3_cell,
    h3_cell_str,
    cell_wkt,
    bookings
from iceberg.gold.geo_demand_h3
