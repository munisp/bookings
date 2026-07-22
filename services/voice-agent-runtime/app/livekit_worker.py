"""LiveKit Agents worker (SPEC §11), pinned to livekit-agents 0.10.x.

Pipeline: silero VAD -> faster-whisper STT (in-process, lazy) ->
OpenAI-compatible LLM (livekit-plugins-openai against LLM_BASE_URL, default
Ollama) -> Piper TTS (HTTP sidecar or subprocess).

Room convention: room name `site-{slug}` binds the session to a tenant's
public site; the server (never the model) resolves the tenant from the slug
(SPEC §1 tenant-safe tool resolution).

Run:  python -m app.livekit_worker start   (via livekit agents CLI)

Version-sensitivity note: the `stt.STT`/`tts.TTS` bridge classes below use
the livekit-agents 0.10.x extension points (`STT.recognize`,
`TTS.synthesize` + `SynthesizedAudio`), verified against the real 0.10.2
package. If a different release reshapes those, the bridges in this file are
the single place to adjust — the pipeline stage implementations in
app/pipeline/ are version-agnostic.
"""

from __future__ import annotations

import uuid
from typing import AsyncIterable

from livekit import agents, rtc
from livekit.agents import llm as lk_llm
from livekit.agents import stt as lk_stt
from livekit.agents import tts as lk_tts
from livekit.agents import utils as lk_utils
from livekit.agents.pipeline import VoicePipelineAgent
from livekit.plugins import openai as lk_openai
from livekit.plugins import silero

from .config import Settings, load_settings
from .dapr_client import DaprClient, DaprError
from .events import new_cloudevent
from .logging import configure_logging, get_logger
from .pipeline.stt import FasterWhisperSTT
from .pipeline.tts import PiperTTS
from .prompts import build_system_prompt
from .session_state import SessionState
from .tenant_context import TenantContext, fetch_tenant_context
from .tools import ToolLayer

log = get_logger("livekit-worker")

ROOM_PREFIX = "site-"


# ---------------------------------------------------------------------------
# Bridges: app pipeline stages -> livekit-agents 0.10 node types
# ---------------------------------------------------------------------------
class WhisperSTTNode(lk_stt.STT):
    """Bridge FasterWhisperSTT into the LiveKit pipeline."""

    def __init__(self, impl: FasterWhisperSTT) -> None:
        super().__init__(
            capabilities=lk_stt.STTCapabilities(
                streaming=False, interim_results=False
            )
        )
        self._impl = impl

    async def recognize(
        self, buffer: lk_utils.AudioBuffer, *, language: str | None = None
    ) -> lk_stt.SpeechEvent:
        frame = lk_utils.merge_frames(buffer)
        text = await self._impl.transcribe_pcm(
            bytes(frame.data),
            sample_rate=frame.sample_rate,
            channels=frame.num_channels,
            language=language,
        )
        return lk_stt.SpeechEvent(
            type=lk_stt.SpeechEventType.FINAL_TRANSCRIPT,
            alternatives=[lk_stt.SpeechData(text=text, language=language or "")],
        )


class PiperTTSNode(lk_tts.TTS):
    """Bridge PiperTTS into the LiveKit pipeline (chunked synthesis)."""

    def __init__(self, impl: PiperTTS) -> None:
        super().__init__(
            capabilities=lk_tts.TTSCapabilities(streaming=False),
            sample_rate=impl.sample_rate,
            num_channels=1,
        )
        self._impl = impl

    def synthesize(self, text: str, **kwargs) -> AsyncIterable[lk_tts.SynthesizedAudio]:
        return self._stream(text)

    async def _stream(self, text: str) -> AsyncIterable[lk_tts.SynthesizedAudio]:
        pcm = await self._impl.synthesize_pcm(text)
        if not pcm:
            return
        frame = rtc.AudioFrame(
            data=pcm,
            sample_rate=self._impl.sample_rate,
            num_channels=1,
            samples_per_channel=len(pcm) // 2,
        )
        yield lk_tts.SynthesizedAudio(
            request_id=str(uuid.uuid4()),
            segment_id=str(uuid.uuid4()),
            frame=frame,
        )


# ---------------------------------------------------------------------------
# Function context: the 6 tools with EXACT names (SPEC §11)
# ---------------------------------------------------------------------------
class ReceptionistFunctions(lk_llm.FunctionContext):
    def __init__(self, tool_layer: ToolLayer) -> None:
        super().__init__()
        self._tools = tool_layer

    @lk_llm.ai_callable(
        description="Get business information: catalog (offerings with ids, durations, prices), team members, timezone, currency and terminology."
    )
    async def get_business_info(self) -> str:
        import json

        result = await self._tools.get_business_info()
        return json.dumps(result, ensure_ascii=False)

    @lk_llm.ai_callable(
        description="Get open appointment slots for an offering with a team member in a time range."
    )
    async def get_availability(
        self,
        offering_id: str,
        team_member_id: str,
        from_iso: str,
        to_iso: str,
    ) -> str:
        import json

        result = await self._tools.get_availability(
            offering_id=offering_id,
            team_member_id=team_member_id,
            from_iso=from_iso,
            to_iso=to_iso,
        )
        return json.dumps(result, ensure_ascii=False)

    @lk_llm.ai_callable(
        description="Book an appointment. Requires the caller's phone number (phone-confirmation policy: the first call returns confirmation_required; read the number back, and call again once the caller confirms)."
    )
    async def book_appointment(
        self,
        offering_id: str,
        team_member_id: str,
        starts_at: str,
        phone: str,
        contact_name: str = "",
        email: str = "",
    ) -> str:
        import json

        result = await self._tools.book_appointment(
            offering_id=offering_id,
            team_member_id=team_member_id,
            starts_at=starts_at,
            phone=phone,
            contact_name=contact_name or None,
            email=email or None,
        )
        return json.dumps(result, ensure_ascii=False)

    @lk_llm.ai_callable(
        description="Look up the caller's upcoming appointments by phone number."
    )
    async def lookup_appointment(self, phone: str) -> str:
        import json

        result = await self._tools.lookup_appointment(phone=phone)
        return json.dumps(result, ensure_ascii=False)

    @lk_llm.ai_callable(
        description="Reschedule an existing booking to a new start time. Requires the caller's phone number."
    )
    async def reschedule_appointment(
        self, booking_id: str, starts_at: str, phone: str
    ) -> str:
        import json

        result = await self._tools.reschedule_appointment(
            booking_id=booking_id, starts_at=starts_at, phone=phone
        )
        return json.dumps(result, ensure_ascii=False)

    @lk_llm.ai_callable(
        description="Cancel an existing booking. Requires the caller's phone number."
    )
    async def cancel_appointment(
        self, booking_id: str, phone: str, reason: str = ""
    ) -> str:
        import json

        result = await self._tools.cancel_appointment(
            booking_id=booking_id, phone=phone, reason=reason or None
        )
        return json.dumps(result, ensure_ascii=False)

    @lk_llm.ai_callable(
        description=(
            "Escalate the conversation to a human staff member (warm handoff). "
            "Creates a LiveKit escalation room, notifies staff and confirms to "
            "the caller. Use when the caller asks for a human, is distressed, "
            "or the request cannot be resolved."
        )
    )
    async def request_human(self, reason: str = "") -> str:
        import json

        result = await self._tools.request_human(reason=reason or None)
        return json.dumps(result, ensure_ascii=False)


# ---------------------------------------------------------------------------
# Session wiring
# ---------------------------------------------------------------------------
async def _publish_lifecycle(
    dapr: DaprClient, settings: Settings, ctx: TenantContext, type_: str, conversation_id: str
) -> None:
    event = new_cloudevent(
        type_=type_,
        subject=ctx.tenant_slug,
        tenant_uuid=ctx.tenant_id,
        data={"conversationId": conversation_id, "channel": "voice", "siteSlug": ctx.site_slug},
    )
    await dapr.publish_best_effort(
        settings.dapr_pubsub, settings.conversation_events_topic, event, kind=type_
    )


def site_slug_from_room(room_name: str) -> str:
    return room_name[len(ROOM_PREFIX):] if room_name.startswith(ROOM_PREFIX) else room_name


async def build_voice_agent(
    settings: Settings,
    dapr: DaprClient,
    site_slug: str,
    conversation_id: str,
) -> tuple[VoicePipelineAgent, TenantContext, SessionState]:
    """Bootstrap tenant context and assemble the VoicePipelineAgent."""
    ctx = await fetch_tenant_context(dapr, settings, site_slug)
    session = SessionState(conversation_id=conversation_id, site_slug=site_slug)
    tool_layer = ToolLayer(dapr=dapr, settings=settings, ctx=ctx, session=session)
    system_prompt = build_system_prompt(ctx, conversation_id=conversation_id)

    chat_ctx = lk_llm.ChatContext().append(role="system", text=system_prompt)
    fnc_ctx = ReceptionistFunctions(tool_layer)

    stt_impl = FasterWhisperSTT(
        model_size=settings.whisper_model,
        device=settings.whisper_device,
        compute_type=settings.whisper_compute_type,
    )
    tts_impl = PiperTTS(
        mode=settings.piper_mode,
        http_url=settings.piper_http_url,
        voice=settings.piper_voice,
        piper_bin=settings.piper_bin,
        model_dir=settings.piper_model_dir,
        sample_rate=settings.piper_sample_rate,
    )

    agent = VoicePipelineAgent(
        vad=silero.VAD.load(),
        stt=WhisperSTTNode(stt_impl),
        llm=lk_openai.LLM(
            model=settings.llm_model,
            base_url=settings.llm_base_url,
            api_key=settings.llm_api_key,
        ),
        tts=PiperTTSNode(tts_impl),
        chat_ctx=chat_ctx,
        fnc_ctx=fnc_ctx,
        allow_interruptions=True,
    )
    return agent, ctx, session


async def entrypoint(ctx: agents.JobContext) -> None:
    settings = load_settings()
    configure_logging(settings.log_level)
    dapr = DaprClient(settings.dapr_base_url, settings.http_timeout_s)

    await ctx.connect()
    room_name = ctx.room.name or ""
    site_slug = site_slug_from_room(room_name)
    conversation_id = str(uuid.uuid4())
    log.info("voice session starting", room=room_name, site_slug=site_slug)

    try:
        agent, tenant_ctx, _session = await build_voice_agent(
            settings, dapr, site_slug, conversation_id
        )
    except DaprError as exc:
        log.error("session bootstrap failed", site_slug=site_slug, error=str(exc))
        await dapr.aclose()
        return

    await _publish_lifecycle(
        dapr,
        settings,
        tenant_ctx,
        "com.opendesk.conversation.SessionStarted",
        conversation_id,
    )

    agent.start(ctx.room)
    # Greet the caller so the conversation opens naturally.
    await agent.say(
        f"Hello, thank you for calling {tenant_ctx.display_name}. How can I help you today?",
        allow_interruptions=True,
    )

    @ctx.room.on("disconnected")
    def _on_disconnected(*args) -> None:  # noqa: ARG001
        import asyncio

        asyncio.get_event_loop().create_task(
            _publish_lifecycle(
                dapr,
                settings,
                tenant_ctx,
                "com.opendesk.conversation.SessionEnded",
                conversation_id,
            )
        )


def main() -> None:
    settings = load_settings()
    configure_logging(settings.log_level)
    log.info("starting livekit agents worker", backend=settings.agent_backend)
    agents.cli.run_app(
        agents.WorkerOptions(
            entrypoint_fnc=entrypoint,
            api_key=settings.livekit_api_key,
            api_secret=settings.livekit_api_secret,
            ws_url=settings.livekit_url,
        )
    )


if __name__ == "__main__":
    main()
