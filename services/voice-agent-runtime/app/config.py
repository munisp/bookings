"""Environment-driven settings (see README.md env table)."""

from __future__ import annotations

import os
from dataclasses import dataclass, field


def _env(key: str, default: str) -> str:
    return os.environ.get(key, default)


def _env_int(key: str, default: int) -> int:
    try:
        return int(os.environ.get(key, str(default)))
    except ValueError:
        return default


@dataclass(frozen=True)
class Settings:
    # Control plane
    port: int = 7006
    log_level: str = "info"

    # Dapr sidecar (SPEC §3: companion daprd container, app-id `voice`)
    dapr_host: str = "daprd-voice"
    dapr_http_port: int = 3500
    dapr_pubsub: str = "pubsub-kafka"
    booking_app_id: str = "booking"
    identity_app_id: str = "identity"
    knowledge_app_id: str = "knowledge"
    booking_commands_topic: str = "opendesk.booking.commands"
    conversation_events_topic: str = "opendesk.conversation.events"

    # LiveKit (SPEC §11: dev keys on 7880)
    livekit_url: str = "ws://livekit:7880"
    livekit_api_key: str = "devkey"
    livekit_api_secret: str = "secret"

    # LLM via OpenAI-compatible endpoint (Ollama default; vLLM/hosted pluggable).
    # Open-model-first (SPEC-W3 §0): default is qwen3:8b on Ollama; the
    # MiniMax-M2 long-context path uses LLM_BASE_URL=https://api.minimax.io/v1
    # + LLM_MODEL=MiniMax-M2 + LLM_API_KEY (see README model-routing table).
    llm_base_url: str = "http://ollama:11434/v1"
    llm_model: str = "qwen3:8b"
    llm_api_key: str = "ollama"  # Ollama ignores the key; hosted providers require one

    # STT (faster-whisper, in-process, lazy-loaded)
    whisper_model: str = "base"
    whisper_device: str = "auto"  # auto|cpu|cuda
    whisper_compute_type: str = "int8"

    # TTS (piper): http sidecar or local subprocess
    piper_mode: str = "http"  # http|subprocess
    piper_http_url: str = "http://piper:5500"
    piper_voice: str = "en_US-lessac-medium"
    piper_bin: str = "piper"
    piper_model_dir: str = "/voices"
    piper_sample_rate: int = 22050

    # Agent backend abstraction (SPEC §11: optional ElevenLabs adapter)
    agent_backend: str = "livekit"  # livekit|elevenlabs
    elevenlabs_api_key: str = ""
    elevenlabs_agent_id: str = ""

    # Session bootstrap
    knowledge_snippet_count: int = 3
    knowledge_query: str = "opening hours services pricing"
    http_timeout_s: float = 15.0

    # Phone-confirmation policy
    phone_confirmation_required: bool = True

    # Warm handoff / whisper-copilot (SPEC-W3 §4, innovation 1): after an
    # escalation the agent keeps drafting suggested replies into the
    # escalation room data channel.
    copilot_mode: bool = True

    # Plugin tools (SPEC-W3 §4, innovation 15): SSRF guard — comma-separated
    # allowlist of hosts pack customTools may call.
    plugin_allowed_hosts: str = "booking,knowledge,identity"

    # Voice biometrics scaffold (SPEC-W3 §4, innovation 2): consent gate,
    # default OFF. Not wired into the audio pipeline (see README).
    voiceprints: bool = False
    voiceprint_threshold: float = 0.75

    extra: dict = field(default_factory=dict)

    @property
    def dapr_base_url(self) -> str:
        return f"http://{self.dapr_host}:{self.dapr_http_port}"


def load_settings() -> Settings:
    return Settings(
        port=_env_int("PORT", 7006),
        log_level=_env("LOG_LEVEL", "info"),
        dapr_host=_env("DAPR_HOST", "daprd-voice"),
        dapr_http_port=_env_int("DAPR_HTTP_PORT", 3500),
        dapr_pubsub=_env("DAPR_PUBSUB_NAME", "pubsub-kafka"),
        booking_app_id=_env("BOOKING_APP_ID", "booking"),
        identity_app_id=_env("IDENTITY_APP_ID", "identity"),
        knowledge_app_id=_env("KNOWLEDGE_APP_ID", "knowledge"),
        booking_commands_topic=_env("BOOKING_COMMANDS_TOPIC", "opendesk.booking.commands"),
        conversation_events_topic=_env(
            "CONVERSATION_EVENTS_TOPIC", "opendesk.conversation.events"
        ),
        livekit_url=_env("LIVEKIT_URL", "ws://livekit:7880"),
        livekit_api_key=_env("LIVEKIT_API_KEY", "devkey"),
        livekit_api_secret=_env("LIVEKIT_API_SECRET", "secret"),
        llm_base_url=_env("LLM_BASE_URL", "http://ollama:11434/v1"),
        llm_model=_env("LLM_MODEL", "qwen3:8b"),
        # Optional pass-through to the OpenAI-compatible client: Ollama
        # ignores it, hosted providers (e.g. MiniMax) require it.
        llm_api_key=_env("LLM_API_KEY", "ollama"),
        whisper_model=_env("WHISPER_MODEL", "base"),
        whisper_device=_env("WHISPER_DEVICE", "auto"),
        whisper_compute_type=_env("WHISPER_COMPUTE_TYPE", "int8"),
        piper_mode=_env("PIPER_MODE", "http"),
        piper_http_url=_env("PIPER_HTTP_URL", "http://piper:5500"),
        piper_voice=_env("PIPER_VOICE", "en_US-lessac-medium"),
        piper_bin=_env("PIPER_BIN", "piper"),
        piper_model_dir=_env("PIPER_MODEL_DIR", "/voices"),
        piper_sample_rate=_env_int("PIPER_SAMPLE_RATE", 22050),
        agent_backend=_env("AGENT_BACKEND", "livekit"),
        elevenlabs_api_key=_env("ELEVENLABS_API_KEY", ""),
        elevenlabs_agent_id=_env("ELEVENLABS_AGENT_ID", ""),
        knowledge_snippet_count=_env_int("KNOWLEDGE_SNIPPET_COUNT", 3),
        knowledge_query=_env("KNOWLEDGE_QUERY", "opening hours services pricing"),
        http_timeout_s=float(os.environ.get("HTTP_TIMEOUT_S", "15")),
        phone_confirmation_required=_env("PHONE_CONFIRMATION_REQUIRED", "true").lower()
        not in ("0", "false", "no"),
        copilot_mode=_env("COPILOT_MODE", "true").lower() not in ("0", "false", "no"),
        plugin_allowed_hosts=_env(
            "PLUGIN_ALLOWED_HOSTS", "booking,knowledge,identity"
        ),
        voiceprints=_env("VOICEPRINTS", "off").lower() in ("1", "on", "true", "yes"),
        voiceprint_threshold=float(os.environ.get("VOICEPRINT_THRESHOLD", "0.75")),
    )
