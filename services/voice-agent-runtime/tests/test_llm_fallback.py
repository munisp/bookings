"""LLM fallback chain (VOICE-SCALING §3): primary failure (429/5xx/timeout/
connection) switches the call to the fallback endpoint; the circuit breaker
routes around a flapping primary for the cooldown window, then probes."""

from __future__ import annotations

from types import SimpleNamespace

import httpx
import openai
import pytest

from app.pipeline.llm import CircuitBreaker, FallbackLLM, is_retryable_llm_error


def _status_error(status: int) -> openai.APIStatusError:
    req = httpx.Request("POST", "http://primary.test/v1/chat/completions")
    resp = httpx.Response(status, request=req)
    return openai.APIStatusError(f"status {status}", response=resp, body=None)


class FakeClock:
    def __init__(self) -> None:
        self.now = 1000.0

    def __call__(self) -> float:
        return self.now

    def advance(self, seconds: float) -> None:
        self.now += seconds


class StubLLM:
    """Duck-typed stand-in for OpenAICompatibleLLM with scripted failures."""

    def __init__(self, exc: Exception | None = None, text: str = "ok") -> None:
        self.exc = exc
        self.text = text
        self.calls = 0

    async def chat_with_tools(self, messages, tools):
        self.calls += 1
        if self.exc is not None:
            raise self.exc
        return SimpleNamespace(content=self.text, tool_calls=None)


def test_retryable_classification():
    req = httpx.Request("POST", "http://x.test")
    assert is_retryable_llm_error(_status_error(429))
    assert is_retryable_llm_error(_status_error(500))
    assert is_retryable_llm_error(_status_error(503))
    assert is_retryable_llm_error(openai.APIConnectionError(request=req))
    assert is_retryable_llm_error(openai.APITimeoutError(request=req))
    # 4xx other than 429 (e.g. auth) must NOT trigger the fallback.
    assert not is_retryable_llm_error(_status_error(401))
    assert not is_retryable_llm_error(ValueError("bad arguments"))


async def test_fallback_on_429():
    primary = StubLLM(exc=_status_error(429))
    fallback = StubLLM(text="from-fallback")
    llm = FallbackLLM(primary, fallback, failure_threshold=3, cooldown_s=60)

    msg = await llm.chat_with_tools([{"role": "user", "content": "hi"}], tools=[])

    assert msg.content == "from-fallback"
    assert primary.calls == 1
    assert fallback.calls == 1


async def test_fallback_on_timeout_and_5xx():
    req = httpx.Request("POST", "http://x.test")
    for exc in (openai.APITimeoutError(request=req), _status_error(502)):
        primary = StubLLM(exc=exc)
        fallback = StubLLM(text="degraded")
        llm = FallbackLLM(primary, fallback)
        msg = await llm.chat_with_tools([], tools=[])
        assert msg.content == "degraded"


async def test_non_retryable_error_raises_without_fallback():
    primary = StubLLM(exc=_status_error(401))
    fallback = StubLLM()
    llm = FallbackLLM(primary, fallback)
    with pytest.raises(openai.APIStatusError):
        await llm.chat_with_tools([], tools=[])
    assert fallback.calls == 0


async def test_circuit_opens_after_threshold_then_probes_and_closes():
    clock = FakeClock()
    primary = StubLLM(exc=_status_error(500))
    fallback = StubLLM(text="fallback")
    llm = FallbackLLM(
        primary, fallback, failure_threshold=3, cooldown_s=60, clock=clock
    )

    # Failures 1..3 each hit the primary, then fall back.
    for _ in range(3):
        msg = await llm.chat_with_tools([], tools=[])
        assert msg.content == "fallback"
    assert primary.calls == 3
    assert llm.breaker.is_open

    # Circuit open: primary is NOT consulted during the cooldown window.
    msg = await llm.chat_with_tools([], tools=[])
    assert msg.content == "fallback"
    assert primary.calls == 3

    # After the cooldown the breaker allows one probe. Primary healed ->
    # success closes the circuit and subsequent calls use the primary again.
    primary.exc = None
    primary.text = "primary-again"
    clock.advance(61)
    msg = await llm.chat_with_tools([], tools=[])
    assert msg.content == "primary-again"
    assert primary.calls == 4
    assert not llm.breaker.is_open

    msg = await llm.chat_with_tools([], tools=[])
    assert msg.content == "primary-again"
    assert primary.calls == 5


async def test_failed_probe_reopens_circuit():
    clock = FakeClock()
    primary = StubLLM(exc=_status_error(500))
    fallback = StubLLM(text="fallback")
    llm = FallbackLLM(
        primary, fallback, failure_threshold=3, cooldown_s=60, clock=clock
    )
    for _ in range(3):
        await llm.chat_with_tools([], tools=[])
    assert primary.calls == 3

    clock.advance(61)
    msg = await llm.chat_with_tools([], tools=[])  # probe fails again
    assert msg.content == "fallback"
    assert primary.calls == 4
    assert llm.breaker.is_open

    # Still inside the new cooldown: no further primary traffic.
    clock.advance(30)
    await llm.chat_with_tools([], tools=[])
    assert primary.calls == 4


class StreamStubLLM:
    """Duck-typed streaming LLM: scripted creation failure or chunk stream."""

    def __init__(self, exc=None, chunks=(), fail_after: int | None = None) -> None:
        self.exc = exc
        self.chunks = list(chunks)
        self.fail_after = fail_after
        self.calls = 0

    async def stream_with_tools(self, messages, tools):
        self.calls += 1
        if self.exc is not None:
            raise self.exc

        chunks, fail_after = self.chunks, self.fail_after

        async def gen():
            for i, c in enumerate(chunks):
                if fail_after is not None and i == fail_after:
                    raise _status_error(500)
                yield c

        return gen()


async def test_stream_falls_back_before_first_chunk():
    primary = StreamStubLLM(exc=_status_error(429))
    fallback = StreamStubLLM(chunks=["c1", "c2"])
    llm = FallbackLLM(primary, fallback)

    stream = await llm.stream_with_tools([], [])
    got = [c async for c in stream]

    assert got == ["c1", "c2"]
    assert primary.calls == 1
    assert fallback.calls == 1


async def test_stream_mid_failure_reraises_without_fallback():
    """Once chunks were yielded, falling back would duplicate content."""
    primary = StreamStubLLM(chunks=["c1", "c2"], fail_after=1)
    fallback = StreamStubLLM(chunks=["x"])
    llm = FallbackLLM(primary, fallback)

    stream = await llm.stream_with_tools([], [])
    got = []
    with pytest.raises(openai.APIStatusError):
        async for c in stream:
            got.append(c)
    assert got == ["c1"]
    assert fallback.calls == 0


def test_circuit_breaker_units():
    clock = FakeClock()
    breaker = CircuitBreaker(failure_threshold=3, cooldown_s=60, clock=clock)
    assert breaker.primary_allowed()
    breaker.record_failure()
    breaker.record_failure()
    assert breaker.primary_allowed()  # below threshold
    breaker.record_failure()
    assert not breaker.primary_allowed()  # open
    clock.advance(59)
    assert not breaker.primary_allowed()
    clock.advance(2)
    assert breaker.primary_allowed()  # probe window
    breaker.record_success()
    assert breaker.primary_allowed()
    assert not breaker.is_open
