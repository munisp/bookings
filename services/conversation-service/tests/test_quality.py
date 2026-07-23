"""Call-quality sentiment enrichment unit tests (STRATEGY §3, Wave 5 #2):
aggregation math, skip-when-no-turns, skip gates, and the enriched event
contract. Offline: fake DB + fake sink, no Kafka/Postgres."""

from __future__ import annotations

import json
import sys
import uuid

import pytest

sys.path.insert(0, ".")

from app.config import Config  # noqa: E402
from app.quality import (  # noqa: E402
    EVENT_TYPE_CALL_QUALITY_ENRICHED,
    CallQualityEnricher,
    build_enriched_event,
)

pytestmark = pytest.mark.asyncio

TENANT = uuid.uuid4()
CONV = uuid.uuid4()


class FakeDB:
    """Sentiment store: {conversation_id: [scores]}; None avg when empty."""

    def __init__(self, sentiments: dict[uuid.UUID, list[float]]):
        self._sentiments = sentiments

    async def sentiment_summary(self, conversation_id, tenant_id):
        scores = self._sentiments.get(conversation_id, [])
        if not scores:
            return None, 0
        return sum(scores) / len(scores), len(scores)


class FakeSink:
    def __init__(self):
        self.published: list[dict] = []

    async def publish(self, record: dict) -> None:
        self.published.append(record)


def session_ended(*, quality: dict | None, tenant_id: uuid.UUID = TENANT,
                  conversation_id: uuid.UUID = CONV) -> bytes:
    data: dict = {"conversationId": str(conversation_id), "channel": "voice",
                  "siteSlug": "acme-salon"}
    if quality is not None:
        data["quality"] = quality
    return json.dumps({
        "specversion": "1.0",
        "id": str(uuid.uuid4()),
        "source": "voice-agent-runtime",
        "type": "com.opendesk.conversation.SessionEnded",
        "subject": "acme-salon",
        "time": "2026-03-01T10:00:00+00:00",
        "tenantid": str(tenant_id),
        "data": data,
    }).encode()


def quality_payload(phone: str | None = "+1555000111") -> dict:
    q: dict = {"duration_s": 95.2, "turn_count": 6, "escalated": False,
               "llm_fallback_used": False}
    if phone is not None:
        q["confirmed_phone"] = phone
    return q


def make_enricher(sentiments: dict[uuid.UUID, list[float]]):
    sink = FakeSink()
    enricher = CallQualityEnricher(Config(), FakeDB(sentiments), sink)
    return enricher, sink


# ------------------------------------------------------------- aggregation
async def test_enriches_with_average_sentiment():
    enricher, sink = make_enricher({CONV: [0.5, -0.25, 1.0]})
    assert await enricher._process(session_ended(quality=quality_payload())) is True
    assert len(sink.published) == 1
    evt = sink.published[0]
    assert evt["type"] == EVENT_TYPE_CALL_QUALITY_ENRICHED
    assert evt["tenantid"] == str(TENANT)
    assert evt["subject"] == "acme-salon"
    data = evt["data"]
    # avg of [0.5, -0.25, 1.0]
    assert data["avg_sentiment"] == pytest.approx(0.4166666, rel=1e-4)
    assert data["turn_sentiment_count"] == 3
    assert data["conversationId"] == str(CONV)
    # the quality payload is preserved and extended, not replaced
    assert data["quality"]["duration_s"] == 95.2
    assert data["quality"]["confirmed_phone"] == "+1555000111"
    assert data["quality"]["avg_sentiment"] == pytest.approx(0.4166666, rel=1e-4)


async def test_aggregation_uses_only_scored_turns():
    # FakeDB mirrors COUNT(sentiment) semantics: un-scored turns are absent
    # from the list, so a 2-of-6 scored call averages the two scores only.
    enricher, sink = make_enricher({CONV: [0.8, -0.2]})
    assert await enricher._process(session_ended(quality=quality_payload())) is True
    data = sink.published[0]["data"]
    assert data["avg_sentiment"] == pytest.approx(0.3)
    assert data["turn_sentiment_count"] == 2


async def test_negative_average_sentiment():
    enricher, sink = make_enricher({CONV: [-0.5, -1.0]})
    assert await enricher._process(session_ended(quality=quality_payload())) is True
    assert sink.published[0]["data"]["avg_sentiment"] == pytest.approx(-0.75)


# ------------------------------------------------------------------- skips
async def test_skips_when_no_sentiment_turns():
    enricher, sink = make_enricher({})  # conversation has no scored turns
    assert await enricher._process(session_ended(quality=quality_payload())) is True
    assert sink.published == []


async def test_skips_when_no_quality():
    enricher, sink = make_enricher({CONV: [0.5]})
    assert await enricher._process(session_ended(quality=None)) is True
    assert sink.published == []


async def test_skips_when_no_confirmed_phone():
    enricher, sink = make_enricher({CONV: [0.5]})
    assert await enricher._process(session_ended(quality=quality_payload(phone=None))) is True
    assert await enricher._process(session_ended(quality=quality_payload(phone="  "))) is True
    assert sink.published == []


async def test_skips_other_event_types():
    enricher, sink = make_enricher({CONV: [0.5]})
    env = json.loads(session_ended(quality=quality_payload()))
    env["type"] = "com.opendesk.conversation.SessionStarted"
    assert await enricher._process(json.dumps(env).encode()) is True
    assert sink.published == []


async def test_skips_poison_payload():
    enricher, sink = make_enricher({CONV: [0.5]})
    assert await enricher._process(b"{not json") is True
    assert sink.published == []


async def test_skips_bad_ids():
    enricher, sink = make_enricher({CONV: [0.5]})
    env = json.loads(session_ended(quality=quality_payload()))
    env["data"]["conversationId"] = "not-a-uuid"
    assert await enricher._process(json.dumps(env).encode()) is True
    assert sink.published == []


# ------------------------------------------------------------ pure builder
def test_build_enriched_event_contract():
    env = json.loads(session_ended(quality=quality_payload()))
    evt = build_enriched_event(env, 0.42, 5)
    assert evt["specversion"] == "1.0"
    assert evt["type"] == EVENT_TYPE_CALL_QUALITY_ENRICHED
    assert evt["data"]["avg_sentiment"] == 0.42
    assert evt["data"]["turn_sentiment_count"] == 5
    # original quality dict on the envelope is not mutated
    assert "avg_sentiment" not in env["data"]["quality"]
