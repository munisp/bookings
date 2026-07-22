"""SSE streaming tool-loop tests (SPEC-W3 §3): run_tool_loop_stream yields
delta events from the LLM stream, executes tool calls through the same
dispatch layer when encountered, and terminates with one final event."""

from __future__ import annotations

import json
from types import SimpleNamespace

import pytest

from app.pipeline.llm import run_tool_loop_stream


def _chunk(content=None, tool_calls=None):
    delta = SimpleNamespace(content=content, tool_calls=tool_calls or [])
    return SimpleNamespace(choices=[SimpleNamespace(delta=delta)])


def _tc(index, tc_id=None, name=None, arguments=None):
    fn = SimpleNamespace(name=name, arguments=arguments)
    return SimpleNamespace(index=index, id=tc_id, function=fn)


class FakeStreamLLM:
    """Plays scripted streaming rounds: each call to stream_with_tools
    returns an async iterator over the queued chunk list."""

    def __init__(self, rounds):
        self._rounds = list(rounds)
        self.calls = 0

    async def stream_with_tools(self, messages, tools):
        self.calls += 1
        chunks = self._rounds.pop(0)

        async def gen():
            for c in chunks:
                yield c

        return gen()


@pytest.mark.asyncio()
async def test_stream_deltas_no_tools():
    llm = FakeStreamLLM([
        [_chunk(content="Hello"), _chunk(content=", world"), _chunk(content="!")],
    ])
    messages = [{"role": "user", "content": "hi"}]
    events = [e async for e in run_tool_loop_stream(llm, messages=messages, tools=[], dispatch=None)]

    kinds = [e["type"] for e in events]
    assert kinds == ["delta", "delta", "delta", "final"]
    assert "".join(e["text"] for e in events[:3]) == "Hello, world!"
    assert events[-1]["text"] == "Hello, world!"
    assert events[-1]["trace"] == []


@pytest.mark.asyncio()
async def test_stream_tool_call_then_final_answer():
    llm = FakeStreamLLM([
        # Round 1: tool call streamed in fragments (id, name, then args pieces).
        [
            _chunk(tool_calls=[_tc(0, tc_id="call_1")]),
            _chunk(tool_calls=[_tc(0, name="get_business_info")]),
            _chunk(tool_calls=[_tc(0, arguments='{"site')]),
            _chunk(tool_calls=[_tc(0, arguments='_slug": "acme"}')]),
        ],
        # Round 2: the final answer, streamed.
        [_chunk(content="We open "), _chunk(content="at 9am.")],
    ])
    dispatched = []

    async def dispatch(name, arguments):
        dispatched.append((name, arguments))
        return {"hours": "9-17"}

    messages = [{"role": "user", "content": "when do you open?"}]
    events = [
        e
        async for e in run_tool_loop_stream(
            llm, messages=messages, tools=[{"type": "function"}], dispatch=dispatch
        )
    ]

    kinds = [e["type"] for e in events]
    assert kinds == ["tool", "delta", "delta", "final"], kinds

    tool_event = events[0]
    assert tool_event["tool"] == "get_business_info"
    assert tool_event["arguments"] == {"site_slug": "acme"}
    assert tool_event["result"] == {"hours": "9-17"}
    assert dispatched == [("get_business_info", {"site_slug": "acme"})]

    assert events[-1]["type"] == "final"
    assert events[-1]["text"] == "We open at 9am."
    assert events[-1]["trace"][0]["tool"] == "get_business_info"

    # History mirrors the buffered loop: assistant w/ tool_calls + tool result.
    roles = [m["role"] for m in messages]
    assert roles == ["user", "assistant", "tool"], roles
    assert messages[1]["tool_calls"][0]["function"]["name"] == "get_business_info"
    assert json.loads(messages[2]["content"]) == {"hours": "9-17"}
    assert messages[2]["tool_call_id"] == "call_1"


@pytest.mark.asyncio()
async def test_stream_max_rounds_fallback():
    tool_round = [_chunk(tool_calls=[_tc(0, tc_id="c", name="noop", arguments="{}")])]
    llm = FakeStreamLLM([tool_round] * 6)

    async def dispatch(name, arguments):
        return {}

    events = [
        e
        async for e in run_tool_loop_stream(
            llm, messages=[{"role": "user", "content": "x"}], tools=[], dispatch=dispatch
        )
    ]
    assert events[-1]["type"] == "final"
    assert "couldn't complete" in events[-1]["text"]
    # fallback text is also streamed as a delta
    assert any(e["type"] == "delta" and "couldn't complete" in e["text"] for e in events)
