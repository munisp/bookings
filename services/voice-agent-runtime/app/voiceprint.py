"""Voice biometrics scaffold (SPEC-W3 §4, innovation 2).

STATUS: **scaffold** — enrollment/verification API surface only. This module
is NOT wired into the audio pipeline (STT path never calls it yet); it exists
so the consent model, storage interface and embedding math are settled before
pipeline integration. See README "Voice biometrics (scaffold)".

Design:
- ``VoiceprintStore`` protocol — pluggable persistence (in-memory dev impl
  included; a Postgres/Dapr-state impl is the production step).
- ``ResemblyzerEncoder`` — optional `resemblyzer` dependency, import-guarded;
  constructing it without the package raises RuntimeError.
- Consent gate — every public function checks ``VOICEPRINTS`` (default off).
  Biometrics are consent-gated: nothing is ever embedded or stored unless the
  operator explicitly opts in, and callers must consent out-of-band (the
  enrollment API records the consent flag alongside the embedding).
"""

from __future__ import annotations

import math
import time
import uuid
from dataclasses import dataclass, field
from typing import Any, Protocol

from .config import Settings
from .logging import get_logger

log = get_logger("voiceprint")


class VoiceprintsDisabled(RuntimeError):
    """Raised when a voiceprint API is called with the consent gate off."""


class VoiceprintStore(Protocol):
    """Persistence interface for enrolled voiceprints."""

    async def enroll(self, record: "VoiceprintRecord") -> None: ...
    async def get(self, user_id: str) -> "VoiceprintRecord | None": ...
    async def delete(self, user_id: str) -> bool: ...


@dataclass
class VoiceprintRecord:
    user_id: str
    embedding: list[float]
    consent: bool  # explicit caller consent recorded at enrollment
    enrolled_at: float = field(default_factory=time.time)
    id: str = field(default_factory=lambda: str(uuid.uuid4()))


class InMemoryVoiceprintStore:
    """Dev-grade store (production: Postgres table or Dapr state store)."""

    def __init__(self) -> None:
        self._records: dict[str, VoiceprintRecord] = {}

    async def enroll(self, record: VoiceprintRecord) -> None:
        self._records[record.user_id] = record

    async def get(self, user_id: str) -> VoiceprintRecord | None:
        return self._records.get(user_id)

    async def delete(self, user_id: str) -> bool:
        return self._records.pop(user_id, None) is not None


def cosine_similarity(a: list[float], b: list[float]) -> float:
    """Cosine similarity in [-1, 1]; 0.0 for empty/degenerate vectors."""
    if not a or not b or len(a) != len(b):
        return 0.0
    dot = sum(x * y for x, y in zip(a, b))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(y * y for y in b))
    if na == 0.0 or nb == 0.0:
        return 0.0
    return dot / (na * nb)


class ResemblyzerEncoder:
    """Speaker-embedding encoder backed by the optional `resemblyzer` package.

    The import is guarded so the runtime works without the heavy optional
    dependency; construction then raises RuntimeError (fail fast at startup,
    never mid-call).
    """

    def __init__(self) -> None:
        try:
            from resemblyzer import VoiceEncoder  # type: ignore[import-not-found]
        except ImportError as exc:
            raise RuntimeError(
                "VOICEPRINTS=on requires the optional 'resemblyzer' package "
                "(pip install resemblyzer)"
            ) from exc
        self._encoder = VoiceEncoder()

    def embed_wav(self, pcm_s16le: bytes, *, sample_rate: int = 16000) -> list[float]:
        """Embed 16-bit mono PCM into a fixed-size speaker vector."""
        import numpy as np

        from resemblyzer import preprocess_wav  # type: ignore[import-not-found]

        samples = np.frombuffer(pcm_s16le, dtype=np.int16).astype(np.float32) / 32768.0
        wav = preprocess_wav(samples, source_sr=sample_rate)
        return [float(x) for x in self._encoder.embed_utterance(wav)]


def _require_enabled(settings: Settings) -> None:
    if not settings.voiceprints:
        raise VoiceprintsDisabled(
            "voice biometrics are disabled (consent gate VOICEPRINTS=off)"
        )


async def enroll_voiceprint(
    settings: Settings,
    store: VoiceprintStore,
    encoder: Any,
    *,
    user_id: str,
    pcm_s16le: bytes,
    consent: bool,
    sample_rate: int = 16000,
) -> VoiceprintRecord:
    """Enroll a caller's voiceprint. Requires VOICEPRINTS=on AND consent=True."""
    _require_enabled(settings)
    if not consent:
        raise VoiceprintsDisabled("caller consent is required to enroll a voiceprint")
    embedding = encoder.embed_wav(pcm_s16le, sample_rate=sample_rate)
    record = VoiceprintRecord(user_id=user_id, embedding=embedding, consent=True)
    await store.enroll(record)
    log.info("voiceprint enrolled", user_id=user_id, dims=len(embedding))
    return record


async def verify_voiceprint(
    settings: Settings,
    store: VoiceprintStore,
    encoder: Any,
    *,
    user_id: str,
    pcm_s16le: bytes,
    sample_rate: int = 16000,
) -> dict[str, Any]:
    """Verify a live sample against the enrolled print for ``user_id``.

    Returns {verified, score, threshold}; verified=False when no enrollment
    exists. Requires VOICEPRINTS=on.
    """
    _require_enabled(settings)
    enrolled = await store.get(user_id)
    if enrolled is None:
        return {
            "verified": False,
            "score": 0.0,
            "threshold": settings.voiceprint_threshold,
            "reason": "no_enrollment",
        }
    embedding = encoder.embed_wav(pcm_s16le, sample_rate=sample_rate)
    score = cosine_similarity(enrolled.embedding, embedding)
    verified = score >= settings.voiceprint_threshold
    log.info("voiceprint verified" if verified else "voiceprint rejected",
             user_id=user_id, score=round(score, 4))
    return {
        "verified": verified,
        "score": score,
        "threshold": settings.voiceprint_threshold,
    }
