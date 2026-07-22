"""STT stage: interface + in-process faster-whisper implementation.

The model is lazy-loaded on first transcription (env WHISPER_MODEL, default
`base`); see README for pre-download / offline notes.
"""

from __future__ import annotations

import asyncio
from typing import Protocol

import numpy as np

from ..logging import get_logger

log = get_logger("stt")


class STTInterface(Protocol):
    async def transcribe_pcm(
        self,
        pcm_s16le: bytes,
        *,
        sample_rate: int,
        channels: int = 1,
        language: str | None = None,
    ) -> str:
        """Transcribe signed-16-bit little-endian PCM audio to text."""
        ...


class FasterWhisperSTT:
    """faster-whisper (CTranslate2) STT, loaded in-process on first use."""

    def __init__(
        self,
        model_size: str = "base",
        device: str = "auto",
        compute_type: str = "int8",
    ) -> None:
        self._model_size = model_size
        self._device = device
        self._compute_type = compute_type
        self._model = None
        self._lock = asyncio.Lock()

    def _load_sync(self):
        # Import deferred: ctranslate2 is heavy and optional at import time.
        from faster_whisper import WhisperModel

        device = self._device
        if device == "auto":
            try:
                import ctranslate2

                device = "cuda" if ctranslate2.get_cuda_device_count() > 0 else "cpu"
            except Exception:  # noqa: BLE001
                device = "cpu"
        log.info(
            "loading whisper model",
            model=self._model_size,
            device=device,
            compute_type=self._compute_type,
        )
        return WhisperModel(self._model_size, device=device, compute_type=self._compute_type)

    async def _ensure_model(self):
        if self._model is not None:
            return self._model
        async with self._lock:
            if self._model is None:
                self._model = await asyncio.to_thread(self._load_sync)
        return self._model

    @staticmethod
    def _pcm_to_float32(
        pcm_s16le: bytes, sample_rate: int, channels: int
    ) -> np.ndarray:
        audio = (
            np.frombuffer(pcm_s16le, dtype=np.int16).astype(np.float32) / 32768.0
        )
        if channels > 1:
            audio = audio.reshape(-1, channels).mean(axis=1)
        if sample_rate != 16000:
            # Linear resample to whisper's 16 kHz (dev-grade; a polyphase
            # resampler would be nicer but adds a dependency).
            ratio = 16000 / sample_rate
            x_old = np.arange(len(audio))
            x_new = np.arange(0, len(audio) - 1, 1 / ratio)
            audio = np.interp(x_new, x_old, audio).astype(np.float32)
        return audio

    async def transcribe_pcm(
        self,
        pcm_s16le: bytes,
        *,
        sample_rate: int,
        channels: int = 1,
        language: str | None = None,
    ) -> str:
        model = await self._ensure_model()
        audio = self._pcm_to_float32(pcm_s16le, sample_rate, channels)
        if len(audio) < 1600:  # < 100 ms
            return ""

        def _run() -> str:
            segments, _info = model.transcribe(
                audio,
                language=language,
                beam_size=1,
                vad_filter=False,  # silero VAD already gates the audio upstream
            )
            return " ".join(seg.text.strip() for seg in segments).strip()

        return await asyncio.to_thread(_run)
