-- gold.agent_containment_rate — share of AI conversations fully handled by the agent
-- (no human handoff) per tenant per day. A conversation is "contained" when it has
-- zero turns with role = 'human_agent' (see assumed bronze schema in dbt_project.yml).
with conversations as (
    select
        tenant_id,
        turn_day as day,
        conversation_id,
        max(case when role = 'human_agent' then 1 else 0 end) as escalated
    from {{ ref('stg_transcripts') }}
    where role in ('user', 'agent', 'human_agent')   -- exclude system/tool plumbing turns
    group by tenant_id, turn_day, conversation_id
)

select
    tenant_id,
    day,
    count(*) as conversations_total,
    count(*) filter (where escalated = 0) as contained_conversations,
    cast(count(*) filter (where escalated = 0) as double) / nullif(count(*), 0) as containment_rate
from conversations
group by tenant_id, day
