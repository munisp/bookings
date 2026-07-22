"""Multi-agent crews: turn-level intent router (SPEC-W3 §4, innovation 6).

Industry packs may declare ``agents: [{id, name, persona, intents: [...]}]``.
Before the main persona answers, each user message is scored against every
agent's intent keyword list (deliberately embedding-free: fast, deterministic,
no extra model dependency). The best-scoring agent — when it scores at all —
becomes the session's active agent and its persona is swapped into the system
prompt; when nothing matches, the session falls back to the base persona.
"""

from __future__ import annotations

import re
from typing import Any

_WORD_RE = re.compile(r"[a-z0-9']+")


def _score(message_tokens: set[str], message_text: str, intent: str) -> int:
    """Score one intent phrase against the message.

    Multi-word intents match as a substring of the normalized message
    (stronger signal, weight 2); single words match token-wise (weight 1).
    """
    intent = intent.strip().lower()
    if not intent:
        return 0
    if " " in intent or "-" in intent:
        return 2 if intent in message_text else 0
    return 1 if intent in message_tokens else 0


def score_agent(message: str, agent: dict[str, Any]) -> int:
    """Total keyword-match score of an agent's intents against a message."""
    text = " ".join(_WORD_RE.findall(message.lower()))
    tokens = set(text.split())
    return sum(_score(tokens, text, str(i)) for i in agent.get("intents") or [])


def route_intent(
    message: str, agents: list[dict[str, Any]]
) -> dict[str, Any] | None:
    """Return the best-matching agent for a user message, or None.

    Ties break deterministically towards the earlier agent in the pack list.
    """
    best: dict[str, Any] | None = None
    best_score = 0
    for agent in agents:
        if not isinstance(agent, dict) or not agent.get("id"):
            continue
        s = score_agent(message, agent)
        if s > best_score:
            best = agent
            best_score = s
    return best


def find_agent(agents: list[dict[str, Any]], agent_id: str | None) -> dict[str, Any] | None:
    """Look up an agent by id (used to resolve session.active_agent)."""
    if not agent_id:
        return None
    for agent in agents:
        if isinstance(agent, dict) and agent.get("id") == agent_id:
            return agent
    return None
