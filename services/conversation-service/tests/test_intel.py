"""Call intelligence unit tests (SPEC-W3 §4, innovation 3): lexicon sentiment
with negation handling, and the enrichment pipeline with a mocked LLM."""

from __future__ import annotations

import sys

import httpx
import pytest

sys.path.insert(0, ".")

from app.config import Config  # noqa: E402
from app.intel import analyze_sentiment, enrich_turn, llm_extract  # noqa: E402

pytestmark = pytest.mark.asyncio


# ---------------------------------------------------------------- sentiment
def test_positive_sentiment():
    r = analyze_sentiment("Thank you, the stylist was wonderful and so friendly!")
    assert r["label"] == "positive"
    assert r["score"] > 0


def test_negative_sentiment():
    r = analyze_sentiment("This is awful, I've been waiting forever and I'm upset")
    assert r["label"] == "negative"
    assert r["score"] < 0


def test_neutral_no_lexicon_words():
    r = analyze_sentiment("I would like to book on Tuesday at ten.")
    assert r["score"] == 0.0
    assert r["label"] == "neutral"


def test_negation_flips_positive_to_negative():
    r = analyze_sentiment("The service was not good at all")
    assert r["label"] == "negative"


def test_negation_flips_negative_to_positive():
    r = analyze_sentiment("Honestly it was not bad at all")
    assert r["label"] == "positive"


def test_negation_window_expires():
    # "good" is more than 3 tokens after "not" -> no flip
    r = analyze_sentiment("I did not in the end think it was good")
    assert r["score"] > 0


def test_balanced_is_neutral():
    r = analyze_sentiment("The stylist was friendly but the wait was terrible")
    assert r["label"] == "neutral"
    assert r["score"] == 0.0


def test_score_bounds():
    for text in ["great great great", "awful terrible worst", "hi"]:
        r = analyze_sentiment(text)
        assert -1.0 <= r["score"] <= 1.0


# ------------------------------------------------------- enrichment pipeline
def _cfg(intel_llm: bool) -> Config:
    return Config(intel_llm=intel_llm, intel_llm_timeout_s=3.0)


def _client(handler) -> httpx.AsyncClient:
    return httpx.AsyncClient(transport=httpx.MockTransport(handler))


async def test_enrich_lexicon_only_by_default():
    result = await enrich_turn("This was wonderful, thanks!", _cfg(intel_llm=False))
    assert result["sentiment"] > 0
    assert result["sentiment_label"] == "positive"
    assert result["intent"] is None
    assert result["entities"] is None


async def test_enrich_with_llm_success():
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.headers.get("authorization") == "Bearer ollama"
        return httpx.Response(200, json={
            "choices": [{"message": {"content":
                '{"intent": "book_appointment", "entities": {"service": "haircut", "day": "Friday"}}'}}]
        })

    result = await enrich_turn(
        "I'd like to book a haircut on Friday please", _cfg(intel_llm=True),
        client=_client(handler),
    )
    assert result["intent"] == "book_appointment"
    assert result["entities"] == {"service": "haircut", "day": "Friday"}


async def test_enrich_llm_timeout_degrades_to_lexicon_only():
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.TimeoutException("timed out")

    result = await enrich_turn(
        "This was wonderful, thanks!", _cfg(intel_llm=True), client=_client(handler)
    )
    assert result["sentiment_label"] == "positive"  # lexicon still ran
    assert result["intent"] is None
    assert result["entities"] is None


async def test_enrich_llm_http_error_degrades():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(500, text="boom")

    result = await enrich_turn("ok", _cfg(intel_llm=True), client=_client(handler))
    assert result["intent"] is None


async def test_llm_extract_tolerates_prose_around_json():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={
            "choices": [{"message": {"content":
                'Sure! Here is the JSON:\n```json\n{"intent": "cancel", "entities": {}}\n```'}}]
        })

    ner = await llm_extract("cancel my visit", _cfg(intel_llm=True), client=_client(handler))
    assert ner == {"intent": "cancel", "entities": {}}


async def test_llm_extract_bad_json_returns_none():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={
            "choices": [{"message": {"content": "I cannot help with that."}}]
        })

    ner = await llm_extract("hi", _cfg(intel_llm=True), client=_client(handler))
    assert ner is None
