# dbt project `opendesk_lakehouse` (SPEC §13 gold layer)

Transforms Iceberg **bronze** tables (raw sinks from analytics-pipeline) into
**silver** staging views and **gold** marts, executed through Trino (dbt-trino).

## Layout

```
models/silver/stg_booking_events.sql      view  — standardized booking events
models/silver/stg_transcripts.sql         view  — standardized transcript turns
models/gold/daily_bookings_per_tenant.sql table — funnel counts/tenant/day
models/gold/revenue_daily.sql             table — captured/refunded/net revenue/tenant/day
models/gold/no_show_rate.sql              table — no-show share/tenant/day
models/gold/agent_containment_rate.sql    table — % conversations with no human handoff
models/*/schema.yml                                sources (assumed bronze schemas) + tests
```

Assumed bronze columns are documented in `dbt_project.yml` (top comment) and
`models/silver/schema.yml`. Containment definition: a conversation is contained when it
has **zero** turns with `role = 'human_agent'`.

## Run

```bash
pip install dbt-core dbt-trino
cd infra/lakehouse/dbt
export DBT_PROFILES_DIR=$PWD
dbt deps
dbt debug            # Trino at localhost:8088 (DBT_TRINO_HOST/PORT overridable)
dbt build            # silver views + gold tables + tests
```

Inside the `opendesk` docker network: `DBT_TRINO_HOST=trino DBT_TRINO_PORT=8080`.

After `dbt build`, gold marts are queryable as `iceberg.gold.*` in Trino and are the
source for the `bookings-analytics` OpenSearch sync (SPEC §10).
