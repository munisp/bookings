"""Voice biometrics scaffold tests (SPEC-W3 §4, innovation 2): consent gate,
store protocol, verification math — all without the resemblyzer dependency."""

from __future__ import annotations

import pytest

from app.config import Settings
from app.voiceprint import (
    InMemoryVoiceprintStore,
    ResemblyzerEncoder,
    VoiceprintRecord,
    VoiceprintsDisabled,
    cosine_similarity,
    enroll_voiceprint,
    verify_voiceprint,
)


class FakeEncoder:
    """Deterministic stand-in for ResemblyzerEncoder."""

    def __init__(self, vector: list[float]) -> None:
        self._vector = vector

    def embed_wav(self, pcm: bytes, *, sample_rate: int = 16000) -> list[float]:
        return list(self._vector)


def _settings(**overrides) -> Settings:
    base = dict(voiceprints=True, voiceprint_threshold=0.75)
    base.update(overrides)
    return Settings(**base)


def test_gate_off_by_default():
    assert Settings().voiceprints is False


def test_cosine_similarity_math():
    assert cosine_similarity([1, 0], [1, 0]) == pytest.approx(1.0)
    assert cosine_similarity([1, 0], [0, 1]) == pytest.approx(0.0)
    assert cosine_similarity([1, 0], [-1, 0]) == pytest.approx(-1.0)
    assert cosine_similarity([], [1]) == 0.0
    assert cosine_similarity([0, 0], [1, 1]) == 0.0


async def test_enroll_requires_gate():
    store = InMemoryVoiceprintStore()
    with pytest.raises(VoiceprintsDisabled):
        await enroll_voiceprint(
            Settings(), store, FakeEncoder([1, 0]),
            user_id="u1", pcm_s16le=b"\x00" * 320, consent=True,
        )


async def test_enroll_requires_consent():
    store = InMemoryVoiceprintStore()
    with pytest.raises(VoiceprintsDisabled):
        await enroll_voiceprint(
            _settings(), store, FakeEncoder([1, 0]),
            user_id="u1", pcm_s16le=b"\x00" * 320, consent=False,
        )


async def test_enroll_and_verify_roundtrip():
    store = InMemoryVoiceprintStore()
    encoder = FakeEncoder([0.6, 0.8])
    settings = _settings()

    record = await enroll_voiceprint(
        settings, store, encoder,
        user_id="u1", pcm_s16le=b"\x00" * 320, consent=True,
    )
    assert record.consent is True
    assert record.embedding == [0.6, 0.8]

    result = await verify_voiceprint(
        settings, store, encoder, user_id="u1", pcm_s16le=b"\x01" * 320
    )
    assert result["verified"] is True
    assert result["score"] == pytest.approx(1.0)
    assert result["threshold"] == 0.75


async def test_verify_rejects_different_voice():
    store = InMemoryVoiceprintStore()
    settings = _settings()
    await enroll_voiceprint(
        settings, store, FakeEncoder([1.0, 0.0]),
        user_id="u1", pcm_s16le=b"\x00" * 320, consent=True,
    )
    result = await verify_voiceprint(
        settings, store, FakeEncoder([0.0, 1.0]), user_id="u1", pcm_s16le=b"\x01" * 320
    )
    assert result["verified"] is False
    assert result["score"] == pytest.approx(0.0)


async def test_verify_without_enrollment():
    store = InMemoryVoiceprintStore()
    result = await verify_voiceprint(
        _settings(), store, FakeEncoder([1, 0]), user_id="ghost", pcm_s16le=b"\x00" * 320
    )
    assert result["verified"] is False
    assert result["reason"] == "no_enrollment"


async def test_store_delete():
    store = InMemoryVoiceprintStore()
    await store.enroll(VoiceprintRecord(user_id="u1", embedding=[1.0], consent=True))
    assert await store.delete("u1") is True
    assert await store.get("u1") is None
    assert await store.delete("u1") is False


def test_resemblyzer_guarded_import_error():
    try:
        import resemblyzer  # noqa: F401
        pytest.skip("resemblyzer installed in this environment")
    except ImportError:
        pass
    with pytest.raises(RuntimeError, match="resemblyzer"):
        ResemblyzerEncoder()
