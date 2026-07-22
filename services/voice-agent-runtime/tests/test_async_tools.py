"""Async tools with filler ack + hard timeout (VOICE-SCALING §5).

Covers: ack spoken/emitted BEFORE the tool result, the fast path cancelling
the ack, the timeout path resolving to a spoken apology (never raising), the
chat-path SSE ack event, and per-industry/terminology ack resolution.
"""

from __future__ import annotations

import asyncio
from types import SimpleNamespace

import pytest

from app import metrics
from app.async_tools import (
    APOLOGY_MESSAGE,
    DEFAULT_ACK,
    AsyncToolRunner,
    ToolAckPolicy,
)
from app.pipeline.llm import run_tool_loop_stream


@pytest.fixture(autouse=True)
def _fresh_registry():
    metrics.reset_registry()
    yield


# ---------------------------------------------------------------------------
# Voice path: ack scheduling vs tool completion ordering
# ---------------------------------------------------------------------------
async def test_ack_spoken_before_slow_tool_result():
    events: list = []

    async def slow_call():
        await asyncio.sleep(0.15)
        events.append("result")
        return {"status": "ok"}

    async def speaker(text: str):
        events.append(("ack", text))

    runner = AsyncToolRunner(timeout_s=4.0, ack_grace_ms=20)
    result = await runner.run(
        "book_appointment", slow_call, ack_policy=ToolAckPolicy(), speaker=speaker
    )

    assert result == {"status": "ok"}
    assert events[0] == ("ack", DEFAULT_ACK)
    assert events[1] == "result"
    assert 'voice_tool_calls_total{result="ok",tool="book_appointment"} 1' in metrics.render()


async def test_fast_tool_cancels_ack():
    spoken: list[str] = []

    async def fast_call():
        await asyncio.sleep(0.01)
        return {"status": "ok"}

    async def speaker(text: str):
        spoken.append(text)

    runner = AsyncToolRunner(timeout_s=4.0, ack_grace_ms=400)
    result = await runner.run(
        "get_availability", fast_call, ack_policy=ToolAckPolicy(), speaker=speaker
    )
    assert result == {"status": "ok"}
    await asyncio.sleep(0.5)  # a stray ack task would have fired by now
    assert spoken == []


async def test_timeout_returns_apology_and_never_raises():
    async def stuck():
        await asyncio.sleep(30)
        return {"status": "ok"}  # pragma: no cover - never reached

    runner = AsyncToolRunner(timeout_s=0.05, ack_grace_ms=10)
    spoken: list[str] = []

    result = await runner.run(
        "get_availability",
        stuck,
        ack_policy=ToolAckPolicy(),
        speaker=lambda text: spoken.append(text) or asyncio.sleep(0),
    )

    assert result["status"] == "timeout"
    assert result["message"] == APOLOGY_MESSAGE
    assert result["spoken"] == APOLOGY_MESSAGE
    # The ack fired (tool outlasted the grace window) before the apology.
    assert spoken == [DEFAULT_ACK]
    assert 'voice_tool_calls_total{result="timeout",tool="get_availability"} 1' in (
        metrics.render()
    )


async def test_tool_exception_returns_apology():
    async def boom():
        raise ConnectionError("dapr unreachable")

    runner = AsyncToolRunner(timeout_s=4.0)
    result = await runner.run("lookup_appointment", boom)
    assert result["status"] == "error"
    assert result["message"] == APOLOGY_MESSAGE
    assert 'voice_tool_calls_total{result="error",tool="lookup_appointment"} 1' in (
        metrics.render()
    )


async def test_fast_tool_skips_ack_even_with_speaker():
    """Tools outside SLOW_TOOLS never get filler, however slow they are."""
    spoken: list[str] = []

    async def call():
        await asyncio.sleep(0.05)
        return {"status": "ok"}

    runner = AsyncToolRunner(timeout_s=4.0, ack_grace_ms=5)
    await runner.run(
        "get_business_info",
        call,
        ack_policy=ToolAckPolicy(),
        speaker=lambda text: spoken.append(text) or asyncio.sleep(0),
    )
    assert spoken == []


# ---------------------------------------------------------------------------
# Chat path: timeout-wrapped dispatch + SSE ack events
# ---------------------------------------------------------------------------
async def test_wrap_dispatch_timeout_returns_apology():
    async def dispatch(name, arguments):
        await asyncio.sleep(30)

    runner = AsyncToolRunner(timeout_s=0.05)
    result = await runner.wrap_dispatch(dispatch)("get_availability", {})
    assert result["status"] == "timeout"
    assert result["message"] == APOLOGY_MESSAGE


def _chunk(content=None, tool_calls=None):
    delta = SimpleNamespace(content=content, tool_calls=tool_calls or [])
    return SimpleNamespace(choices=[SimpleNamespace(delta=delta)], usage=None)


def _tc(index, tc_id=None, name=None, arguments=None):
    fn = SimpleNamespace(name=name, arguments=arguments)
    return SimpleNamespace(index=index, id=tc_id, function=fn)


class _FakeStreamLLM:
    def __init__(self, rounds):
        self._rounds = list(rounds)

    async def stream_with_tools(self, messages, tools):
        chunks = self._rounds.pop(0)

        async def gen():
            for c in chunks:
                yield c

        return gen()


async def test_chat_path_ack_event_before_tool_result():
    llm = _FakeStreamLLM(
        [
            [_chunk(tool_calls=[_tc(0, tc_id="c1", name="book_appointment", arguments="{}")])],
            [_chunk(content="Booked!")],
        ]
    )

    async def dispatch(name, arguments):
        return {"status": "accepted"}

    policy = ToolAckPolicy()
    events = [
        e
        async for e in run_tool_loop_stream(
            llm,
            messages=[{"role": "user", "content": "book me"}],
            tools=[],
            dispatch=dispatch,
            ack_for_tool=policy.ack_for,
        )
    ]
    kinds = [e["type"] for e in events]
    assert kinds == ["ack", "tool", "delta", "final"], kinds
    assert events[0]["tool"] == "book_appointment"
    assert events[0]["text"] == DEFAULT_ACK


async def test_chat_path_no_ack_for_fast_tool():
    llm = _FakeStreamLLM(
        [
            [_chunk(tool_calls=[_tc(0, tc_id="c1", name="get_business_info", arguments="{}")])],
            [_chunk(content="We open at 9.")],
        ]
    )

    async def dispatch(name, arguments):
        return {"hours": "9-17"}

    policy = ToolAckPolicy()
    events = [
        e
        async for e in run_tool_loop_stream(
            llm,
            messages=[{"role": "user", "content": "hours?"}],
            tools=[],
            dispatch=dispatch,
            ack_for_tool=policy.ack_for,
        )
    ]
    assert [e["type"] for e in events] == ["tool", "delta", "final"]


# ---------------------------------------------------------------------------
# Ack policy resolution
# ---------------------------------------------------------------------------
def test_ack_policy_industry_default():
    policy = ToolAckPolicy(industry="salon")
    assert policy.ack_for("get_availability") == "Let me take a look at the book for you…"
    assert policy.ack_for("get_business_info") is None


def test_ack_policy_fallback_default():
    assert ToolAckPolicy().ack_for("book_appointment") == DEFAULT_ACK


def test_ack_policy_terminology_overrides():
    per_tool = ToolAckPolicy(
        industry="clinic",
        terminology={"tool_ack": {"book_appointment": "Booking that for you now…"}},
    )
    assert per_tool.ack_for("book_appointment") == "Booking that for you now…"
    # Non-overridden slow tools keep the industry default.
    assert per_tool.ack_for("get_availability") == (
        "One moment while I check our scheduling system…"
    )
    global_override = ToolAckPolicy(terminology={"tool_ack": "Un momento…"})
    assert global_override.ack_for("reschedule_appointment") == "Un momento…"
