"""Multi-agent crew intent router tests (SPEC-W3 §4, innovation 6)."""

from __future__ import annotations

from app.intent_router import find_agent, route_intent, score_agent

CLINIC_AGENTS = [
    {
        "id": "intake-nurse",
        "name": "Intake Nurse",
        "persona": "You guide new patients through intake.",
        "intents": ["intake", "new patient", "first visit", "form", "symptoms"],
    },
    {
        "id": "insurance-checker",
        "name": "Insurance Checker",
        "persona": "You answer coverage and billing questions.",
        "intents": ["insurance", "coverage", "billing", "claim", "cost"],
    },
]


def test_routes_single_word_intent():
    agent = route_intent("Do you accept my insurance?", CLINIC_AGENTS)
    assert agent is not None and agent["id"] == "insurance-checker"


def test_routes_multiword_intent_stronger():
    agent = route_intent("Hi, I am a new patient and would like a first visit", CLINIC_AGENTS)
    assert agent is not None and agent["id"] == "intake-nurse"


def test_multiword_beats_single_word():
    # "new patient" (weight 2) must beat a single "cost" (weight 1) mention.
    agent = route_intent("I am a new patient, what does it cost?", CLINIC_AGENTS)
    assert agent is not None and agent["id"] == "intake-nurse"


def test_no_match_returns_none():
    assert route_intent("What time do you open tomorrow?", CLINIC_AGENTS) is None


def test_empty_agents_returns_none():
    assert route_intent("insurance please", []) is None


def test_tie_breaks_to_first_listed():
    agents = [
        {"id": "a", "name": "A", "persona": "", "intents": ["help"]},
        {"id": "b", "name": "B", "persona": "", "intents": ["help"]},
    ]
    agent = route_intent("I need help", agents)
    assert agent is not None and agent["id"] == "a"


def test_malformed_agents_skipped():
    agents = [{"name": "no id"}, "garbage", None]
    assert route_intent("insurance", agents) is None  # type: ignore[arg-type]


def test_score_agent_case_insensitive():
    assert score_agent("INSURANCE Coverage!", CLINIC_AGENTS[1]) == 2


def test_find_agent():
    assert find_agent(CLINIC_AGENTS, "intake-nurse")["name"] == "Intake Nurse"
    assert find_agent(CLINIC_AGENTS, "nope") is None
    assert find_agent(CLINIC_AGENTS, None) is None
