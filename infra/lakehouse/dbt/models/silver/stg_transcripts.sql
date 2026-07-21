-- stg_transcripts: standardized view over bronze.transcripts.
-- `role` is lowercased so gold models can rely on: user|agent|human_agent|system|tool.
with source as (
    select * from {{ source('bronze', 'transcripts') }}
),

standardized as (
    select
        conversation_id,
        tenant_id,
        lower(trim(role)) as role,
        text,
        ts,
        audio_url,
        cast(date_trunc('day', ts) as date) as turn_day
    from source
    where conversation_id is not null
      and tenant_id is not null
      and ts is not null
)

select * from standardized
