"""System prompt construction (SPEC §11).

Tenant terminology/timezone/currency/locale are injected as dynamic variables
(mirroring the baseline Switchboard's dynamic-variables approach): the prompt
is rendered per session from the bootstrapped TenantContext.
"""

from __future__ import annotations

import json

from .tenant_context import TenantContext

_TEMPLATE = """You are {agent_name}, the AI receptionist for {business_name}.

STYLE
- Warm, concise, professional. Short spoken sentences; no markdown, no lists
  longer than three items when speaking.
- Never invent catalog facts, prices or availability. Use the tools.
- Locale: {locale}. Currency: {currency}. The business timezone is {timezone};
  reason about opening hours and dates in that timezone.

TERMINOLOGY (use these words with the caller)
{terminology}

CATALOG (offerings)
{offerings}

TEAM
{team_members}

KNOWLEDGE
{knowledge}

TOOLS
You have exactly these tools: get_business_info, get_availability,
book_appointment, lookup_appointment, reschedule_appointment,
cancel_appointment, request_human{extra_tool_names}.
- Use get_availability before offering times; quote times in {timezone}.
- book_appointment requires: offering_id, team_member_id, starts_at (RFC3339),
  and the caller's phone number.
- request_human: use it when the caller explicitly asks for a human, is
  distressed, or you cannot resolve their request after two attempts.

PHONE-CONFIRMATION POLICY (hard rule, enforced server-side)
- Before ANY booking, lookup, reschedule or cancellation you MUST collect the
  caller's phone number, read it back digit by digit, and get an explicit
  "yes".
- If a tool answers "confirmation_required", read the phone number back and
  ask the caller to confirm; when they confirm, call the tool again with the
  same number.
- Never claim a booking exists until the tool confirms it was accepted.

CALLER CONTEXT
- conversation_id: {conversation_id}
- site: {site_slug}
"""


def build_system_prompt(
    ctx: TenantContext,
    *,
    conversation_id: str,
    agent_name: str = "the front-desk assistant",
    active_agent: dict[str, Any] | None = None,
    extra_tool_names: list[str] | None = None,
) -> str:
    """Render the system prompt.

    SPEC-W3 §4 innovation 6: when ``active_agent`` (a pack ``agents`` entry)
    is set, the specialist's name/persona steer this turn; otherwise the base
    persona applies (fallback). ``extra_tool_names`` lists pack plugin tools
    registered alongside the built-ins.
    """
    terminology = (
        json.dumps(ctx.terminology, ensure_ascii=False, indent=2)
        if ctx.terminology
        else "(default terminology)"
    )
    knowledge = (
        "\n".join(f"- {s}" for s in ctx.knowledge_snippets)
        if ctx.knowledge_snippets
        else "- (no extra knowledge available)"
    )
    if active_agent is not None:
        agent_name = str(active_agent.get("name") or agent_name)
    extra_names = "".join(f", {n}" for n in (extra_tool_names or []))
    prompt = _TEMPLATE.format(
        agent_name=agent_name,
        business_name=ctx.display_name or ctx.site_slug,
        locale=ctx.locale,
        currency=ctx.currency,
        timezone=ctx.timezone,
        terminology=terminology,
        offerings=ctx.offering_summary(),
        team_members=ctx.team_summary(),
        knowledge=knowledge,
        conversation_id=conversation_id,
        site_slug=ctx.site_slug,
        extra_tool_names=extra_names,
    )
    # SPEC-CRM §C4: append the industry pack persona when the tenant's pack
    # provides one (guarded — absent for tenants without a resolved pack).
    if ctx.agent_persona:
        prompt += f"\nINDUSTRY PERSONA (follow this guidance on tone, policies and domain knowledge)\n{ctx.agent_persona}\n"
    if active_agent is not None:
        persona = str(active_agent.get("persona") or "").strip()
        if persona:
            prompt += (
                f"\nSPECIALIST AGENT ACTIVE — you are now speaking as "
                f"{active_agent.get('name')}. Follow this specialist persona "
                f"for this part of the conversation; the base receptionist "
                f"rules above still apply.\n{persona}\n"
            )
    elif ctx.agents:
        roster = "\n".join(
            f"- {a.get('name')} (id {a.get('id')}): handles {', '.join(str(i) for i in (a.get('intents') or [])[:6])}"
            for a in ctx.agents
            if isinstance(a, dict)
        )
        if roster:
            prompt += (
                "\nSPECIALIST AGENTS (the platform routes the conversation "
                "automatically when the caller's intent matches; you do not "
                "need to transfer explicitly)\n" + roster + "\n"
            )
    return prompt
