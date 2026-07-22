"""Swappable pipeline stages (SPEC §11): VAD -> STT -> LLM -> TTS.

Each stage sits behind a small interface so implementations can be replaced
(e.g. whisper.cpp for STT, vLLM for the LLM, a hosted TTS) without touching
the agent wiring.
"""

from .llm import LLMInterface, OpenAICompatibleLLM
from .stt import FasterWhisperSTT, STTInterface
from .tts import PiperTTS, TTSInterface

__all__ = [
    "FasterWhisperSTT",
    "LLMInterface",
    "OpenAICompatibleLLM",
    "PiperTTS",
    "STTInterface",
    "TTSInterface",
]
