"""Inference-plane metrics (VOICE-SCALING §3): hand-rolled Prometheus text
exposition contains every voice_* series after mocked STT/TTS/LLM calls."""

from __future__ import annotations

from types import SimpleNamespace

import pytest

from app import metrics
from app.pipeline.stt import FasterWhisperSTT
from app.pipeline.tts import PiperTTS


@pytest.fixture(autouse=True)
def _fresh_registry():
    metrics.reset_registry()
    yield


def test_exposition_contains_all_series_after_mocked_calls():
    registry = metrics.get_registry()
    registry.stt_latency.observe(0.25)
    registry.llm_latency.observe(1.5)
    registry.llm_tokens.inc(12, kind="prompt")
    registry.llm_tokens.inc(7, kind="completion")
    registry.tts_latency.observe(0.4)
    registry.tool_calls.inc(tool="get_availability", result="ok")
    registry.active_sessions.inc()

    text = metrics.render()

    # Histograms: cumulative buckets + sum/count.
    assert 'voice_stt_latency_seconds_bucket{le="0.25"} 1' in text
    assert 'voice_stt_latency_seconds_bucket{le="+Inf"} 1' in text
    assert "voice_stt_latency_seconds_count 1" in text
    assert 'voice_llm_latency_seconds_bucket{le="2"} 1' in text
    assert "voice_tts_latency_seconds_count 1" in text
    # Counters with labels.
    assert 'voice_llm_tokens_total{kind="prompt"} 12' in text
    assert 'voice_llm_tokens_total{kind="completion"} 7' in text
    assert 'voice_tool_calls_total{result="ok",tool="get_availability"} 1' in text
    # Gauge.
    assert "voice_active_sessions 1" in text
    # TYPE/HELP headers for the exposition format.
    assert "# TYPE voice_stt_latency_seconds histogram" in text
    assert "# TYPE voice_active_sessions gauge" in text


def test_gauge_inc_dec():
    registry = metrics.get_registry()
    registry.active_sessions.inc()
    registry.active_sessions.inc()
    registry.active_sessions.dec()
    assert "voice_active_sessions 1" in metrics.render()


def test_histogram_timer_context_manager():
    with metrics.get_registry().stt_latency.time():
        pass
    assert "voice_stt_latency_seconds_count 1" in metrics.render()


async def test_stt_call_site_instrumented():
    stt = FasterWhisperSTT(model_size="base")

    class _FakeModel:
        def transcribe(self, audio, **kwargs):
            return [SimpleNamespace(text=" hello world ")], None

    stt._model = _FakeModel()  # skip the real whisper load
    pcm = b"\x00\x00" * 16000  # 1s of 16 kHz s16le silence
    text = await stt.transcribe_pcm(pcm, sample_rate=16000)

    assert text == "hello world"
    assert "voice_stt_latency_seconds_count 1" in metrics.render()


async def test_tts_call_site_instrumented(monkeypatch):
    tts = PiperTTS(mode="http", http_url="http://piper:5500")

    async def _fake_http(text):
        return b"\x00\x00" * 100, 22050

    monkeypatch.setattr(tts, "_synthesize_http", _fake_http)
    pcm = await tts.synthesize_pcm("hello")

    assert pcm
    assert "voice_tts_latency_seconds_count 1" in metrics.render()


async def test_llm_call_site_instrumented():
    from app.pipeline.llm import OpenAICompatibleLLM

    llm = OpenAICompatibleLLM(base_url="http://unused:1/v1", model="m")

    usage = SimpleNamespace(prompt_tokens=9, completion_tokens=4)

    class _FakeCompletions:
        async def create(self, **kwargs):
            return SimpleNamespace(
                choices=[SimpleNamespace(message=SimpleNamespace(content="hi", tool_calls=None))],
                usage=usage,
            )

    llm._client = SimpleNamespace(chat=SimpleNamespace(completions=_FakeCompletions()))
    msg = await llm.chat_with_tools([{"role": "user", "content": "hi"}], tools=[])

    assert msg.content == "hi"
    text = metrics.render()
    assert "voice_llm_latency_seconds_count 1" in text
    assert 'voice_llm_tokens_total{kind="prompt"} 9' in text
    assert 'voice_llm_tokens_total{kind="completion"} 4' in text
