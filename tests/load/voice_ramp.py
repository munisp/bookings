#!/usr/bin/env python3
"""Voice load ramp harness (VOICE-SCALING §7).

Spawns N simulated callers against a LIVE OpenDesk voice stack: each caller
joins its own LiveKit room (`loadtest-{i}`) and publishes looped speech-like
audio (a generated tone+noise WAV), which triggers the voice worker's agent
job per room. Concurrency ramps in stages (default 5 → 10 → 25 → 50) with a
hold at each stage while the harness scrapes the voice control plane's
Prometheus endpoint and reports per-layer saturation + the first layer that
starts degrading — "the test is done when you know which layer fails next
and at what number."

REQUIREMENTS (not a unit test — see README.md in this directory):
  - running stack: `make up-voice` (livekit, voice control plane :7006,
    voice worker(s), ollama/piper, dapr sidecars)
  - python deps: `pip install livekit-api livekit` (rtc SDK)

Example:
  python3 tests/load/voice_ramp.py \
      --livekit-url ws://localhost:7880 --api-key devkey --api-secret secret \
      --ramp 5,10,25,50 --hold-s 60 --metrics-url http://localhost:7006/metrics
"""

from __future__ import annotations

import argparse
import asyncio
import io
import math
import random
import re
import struct
import sys
import time
import urllib.request
import wave
from dataclasses import dataclass, field

# ---------------------------------------------------------------------------
# Audio fixture: programmatic tone+noise WAV (speech-like energy envelope)
# ---------------------------------------------------------------------------
def make_speech_like_wav(seconds: float = 6.0, sample_rate: int = 16000) -> bytes:
    """440 Hz tone with amplitude wobble + light noise, s16le mono WAV bytes.

    Deterministic seed so every stage publishes identical audio.
    """
    rng = random.Random(42)
    frames = int(seconds * sample_rate)
    buf = io.BytesIO()
    with wave.open(buf, "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)
        wf.setframerate(sample_rate)
        samples = bytearray()
        for i in range(frames):
            t = i / sample_rate
            envelope = 0.55 + 0.45 * math.sin(2 * math.pi * 2.3 * t)  # syllable-ish wobble
            tone = math.sin(2 * math.pi * 440 * t) * envelope
            noise = (rng.random() - 0.5) * 0.15
            value = int(max(-1.0, min(1.0, tone * 0.7 + noise)) * 32767)
            samples += struct.pack("<h", value)
        wf.writeframes(bytes(samples))
    return buf.getvalue()


# ---------------------------------------------------------------------------
# Minimal Prometheus text-format parsing (no client dependency)
# ---------------------------------------------------------------------------
_SAMPLE_RE = re.compile(r"^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{[^}]*\})?\s+(-?[0-9.eE+-]+)$")


def scrape_text(url: str, timeout: float = 5.0) -> str:
    with urllib.request.urlopen(url, timeout=timeout) as resp:  # noqa: S310 - ops tool
        return resp.read().decode("utf-8", errors="replace")


def parse_samples(text: str) -> dict[str, float]:
    """Fully-qualified sample name (incl. {labels}) -> value."""
    out: dict[str, float] = {}
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        m = _SAMPLE_RE.match(line.strip())
        if m:
            name = m.group(1) + (m.group(2) or "")
            try:
                out[name] = float(m.group(3))
            except ValueError:
                pass
    return out


@dataclass
class LayerSnapshot:
    active_sessions: float = 0.0
    stt_count: float = 0.0
    stt_sum: float = 0.0
    llm_count: float = 0.0
    llm_sum: float = 0.0
    tts_count: float = 0.0
    tts_sum: float = 0.0
    tool_ok: float = 0.0
    tool_error: float = 0.0
    tool_timeout: float = 0.0

    @classmethod
    def from_samples(cls, s: dict[str, float]) -> "LayerSnapshot":
        tool = {"ok": 0.0, "error": 0.0, "timeout": 0.0}
        for name, value in s.items():
            if name.startswith("voice_tool_calls_total{"):
                for kind in tool:
                    if f'result="{kind}"' in name:
                        tool[kind] += value
        return cls(
            active_sessions=s.get("voice_active_sessions", 0.0),
            stt_count=s.get("voice_stt_latency_seconds_count", 0.0),
            stt_sum=s.get("voice_stt_latency_seconds_sum", 0.0),
            llm_count=s.get("voice_llm_latency_seconds_count", 0.0),
            llm_sum=s.get("voice_llm_latency_seconds_sum", 0.0),
            tts_count=s.get("voice_tts_latency_seconds_count", 0.0),
            tts_sum=s.get("voice_tts_latency_seconds_sum", 0.0),
            tool_ok=tool["ok"],
            tool_error=tool["error"],
            tool_timeout=tool["timeout"],
        )


def _rate(delta_sum: float, delta_count: float) -> float | None:
    return (delta_sum / delta_count) if delta_count > 0 else None


@dataclass
class LayerThresholds:
    """Saturation marks per layer (avg latency seconds / error ratios)."""

    stt_avg_s: float = 2.0
    llm_avg_s: float = 4.0
    tts_avg_s: float = 1.5
    tool_bad_ratio: float = 0.05  # (error + timeout) / total tool calls


@dataclass
class StageReport:
    concurrency: int
    hold_s: float
    stt_avg: float | None = None
    llm_avg: float | None = None
    tts_avg: float | None = None
    tool_bad_ratio: float | None = None
    active_sessions: float = 0.0
    degrading: list[str] = field(default_factory=list)


def diff_stage(
    concurrency: int, hold_s: float, before: LayerSnapshot, after: LayerSnapshot
) -> StageReport:
    rep = StageReport(
        concurrency=concurrency,
        hold_s=hold_s,
        stt_avg=_rate(after.stt_sum - before.stt_sum, after.stt_count - before.stt_count),
        llm_avg=_rate(after.llm_sum - before.llm_sum, after.llm_count - before.llm_count),
        tts_avg=_rate(after.tts_sum - before.tts_sum, after.tts_count - before.tts_count),
        active_sessions=after.active_sessions,
    )
    d_ok = after.tool_ok - before.tool_ok
    d_err = after.tool_error - before.tool_error
    d_to = after.tool_timeout - before.tool_timeout
    total = d_ok + d_err + d_to
    if total > 0:
        rep.tool_bad_ratio = (d_err + d_to) / total
    return rep


# ---------------------------------------------------------------------------
# Simulated caller (livekit rtc, imported lazily so py_compile needs no deps)
# ---------------------------------------------------------------------------
class SimulatedCaller:
    def __init__(self, index: int, url: str, api_key: str, api_secret: str, wav: bytes) -> None:
        self.index = index
        self.room_name = f"loadtest-{index}"
        self._url = url
        self._api_key = api_key
        self._api_secret = api_secret
        self._wav = wav
        self._stop = asyncio.Event()
        self._task: asyncio.Task | None = None

    async def start(self) -> None:
        from livekit import api as lk_api  # lazy: needs the live stack deps

        token = (
            lk_api.AccessToken(self._api_key, self._api_secret)
            .with_identity(f"loadgen-{self.index}")
            .with_name(f"Load caller {self.index}")
            .with_grants(lk_api.VideoGrants(room_join=True, room=self.room_name))
            .to_jwt()
        )
        self._task = asyncio.create_task(self._run(token))

    async def _run(self, token: str) -> None:
        from livekit import rtc

        room = rtc.Room()
        try:
            await room.connect(self._url, token)
            source = rtc.AudioSource(16000, 1)
            track = rtc.LocalAudioTrack.create_audio_track("sim-speech", source)
            await room.local_participant.publish_track(track)

            with wave.open(io.BytesIO(self._wav), "rb") as wf:
                pcm = wf.readframes(wf.getnframes())

            samples_per_frame = 1600  # 100 ms at 16 kHz
            bytes_per_frame = samples_per_frame * 2
            while not self._stop.is_set():
                for off in range(0, len(pcm) - bytes_per_frame + 1, bytes_per_frame):
                    if self._stop.is_set():
                        break
                    frame = rtc.AudioFrame(
                        data=pcm[off : off + bytes_per_frame],
                        sample_rate=16000,
                        num_channels=1,
                        samples_per_channel=samples_per_frame,
                    )
                    await source.capture_frame(frame)
                    await asyncio.sleep(0.1)  # real-time pacing
        except asyncio.CancelledError:
            raise
        except Exception as exc:  # noqa: BLE001 - reported, ramp continues
            print(f"  [caller {self.index}] disconnected: {exc}", flush=True)
        finally:
            try:
                await room.disconnect()
            except Exception:  # noqa: BLE001
                pass

    async def stop(self) -> None:
        self._stop.set()
        if self._task is not None:
            self._task.cancel()
            try:
                await self._task
            except Exception:  # noqa: BLE001 - includes CancelledError on 3.8+ semantics
                pass


async def create_rooms(count: int, url: str, api_key: str, api_secret: str) -> None:
    """Pre-create the ramp rooms via livekit-api (server-side room records)."""
    from livekit import api as lk_api

    http_url = url.replace("ws://", "http://").replace("wss://", "https://")
    client = lk_api.LiveKitAPI(http_url, api_key, api_secret)
    try:
        for i in range(count):
            await client.room.create_room(
                lk_api.CreateRoomRequest(name=f"loadtest-{i}", empty_timeout=30)
            )
    finally:
        await client.aclose()


# ---------------------------------------------------------------------------
# Optional Prometheus server instant query (per-layer cross-check)
# ---------------------------------------------------------------------------
def prometheus_instant(prom_url: str, query: str) -> float | None:
    try:
        text = scrape_text(f"{prom_url.rstrip('/')}/api/v1/query?query={query}")
        m = re.search(r'"value":\[\s*[0-9.]+,\s*"(-?[0-9.eE+-]+)"', text)
        return float(m.group(1)) if m else None
    except Exception:  # noqa: BLE001 - optional source
        return None


# ---------------------------------------------------------------------------
# Ramp driver + report
# ---------------------------------------------------------------------------
async def run_ramp(args) -> int:
    wav = make_speech_like_wav()
    top = max(args.ramp)

    if not args.skip_room_create:
        print(f"creating {top} rooms via livekit-api …", flush=True)
        await create_rooms(top, args.livekit_url, args.api_key, args.api_secret)

    callers: list[SimulatedCaller] = []
    reports: list[StageReport] = []
    first_degrading: tuple[int, str] | None = None

    try:
        for stage_n in args.ramp:
            while len(callers) < stage_n:
                caller = SimulatedCaller(
                    len(callers), args.livekit_url, args.api_key, args.api_secret, wav
                )
                await caller.start()
                callers.append(caller)
            print(
                f"stage {stage_n}: {len(callers)} concurrent callers, "
                f"holding {args.hold_s}s …",
                flush=True,
            )
            await asyncio.sleep(args.warmup_s)  # let sessions attach before sampling
            before = LayerSnapshot.from_samples(
                parse_samples(scrape_text(args.metrics_url))
            )
            await asyncio.sleep(args.hold_s)
            after = LayerSnapshot.from_samples(
                parse_samples(scrape_text(args.metrics_url))
            )

            rep = diff_stage(stage_n, args.hold_s, before, after)
            if rep.stt_avg is not None and rep.stt_avg > args.thresh_stt:
                rep.degrading.append("stt")
            if rep.llm_avg is not None and rep.llm_avg > args.thresh_llm:
                rep.degrading.append("llm")
            if rep.tts_avg is not None and rep.tts_avg > args.thresh_tts:
                rep.degrading.append("tts")
            if (
                rep.tool_bad_ratio is not None
                and rep.tool_bad_ratio > args.thresh_tool_ratio
            ):
                rep.degrading.append("application(tools)")
            if rep.degrading and first_degrading is None:
                first_degrading = (stage_n, rep.degrading[0])
            reports.append(rep)
            print(
                f"  stt_avg={_fmt(rep.stt_avg)} llm_avg={_fmt(rep.llm_avg)} "
                f"tts_avg={_fmt(rep.tts_avg)} tool_bad={_fpct(rep.tool_bad_ratio)} "
                f"sessions={rep.active_sessions:g}"
                + (f"  DEGRADING: {', '.join(rep.degrading)}" if rep.degrading else ""),
                flush=True,
            )
    finally:
        print("stopping callers …", flush=True)
        await asyncio.gather(*(c.stop() for c in callers))

    _print_report(reports, first_degrading, args)

    if args.prometheus_url:
        sessions = prometheus_instant(args.prometheus_url, "voice_active_sessions")
        print(f"prometheus cross-check: voice_active_sessions={sessions}")

    return 1 if first_degrading is None else 0


def _fmt(value: float | None) -> str:
    return "n/a" if value is None else f"{value:.3f}s"


def _fpct(value: float | None) -> str:
    return "n/a" if value is None else f"{value * 100:.1f}%"


def _print_report(
    reports: list[StageReport], first_degrading: tuple[int, str] | None, args
) -> None:
    print("\n=== per-layer saturation report ===")
    print(
        f"{'conc':>5} {'stt_avg':>9} {'llm_avg':>9} {'tts_avg':>9} "
        f"{'tool_bad':>9} {'sessions':>9}  degrading"
    )
    for rep in reports:
        print(
            f"{rep.concurrency:>5} {_fmt(rep.stt_avg):>9} {_fmt(rep.llm_avg):>9} "
            f"{_fmt(rep.tts_avg):>9} {_fpct(rep.tool_bad_ratio):>9} "
            f"{rep.active_sessions:>9.0f}  {', '.join(rep.degrading) or '-'}"
        )
    print(
        "\nthresholds: stt>{0}s llm>{1}s tts>{2}s tool-bad>{3:.0%}".format(
            args.thresh_stt, args.thresh_llm, args.thresh_tts, args.thresh_tool_ratio
        )
    )
    if first_degrading is None:
        print("RESULT: no layer degraded within the ramp — raise the ceiling.")
    else:
        n, layer = first_degrading
        print(f"RESULT: first-degrading layer = {layer} at ~{n} concurrent calls.")


def parse_args(argv: list[str] | None = None):
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--livekit-url", default="ws://localhost:7880")
    p.add_argument("--api-key", default="devkey")
    p.add_argument("--api-secret", default="secret")
    p.add_argument(
        "--ramp",
        default="5,10,25,50",
        help="comma-separated concurrency stages (default 5,10,25,50)",
    )
    p.add_argument("--hold-s", type=float, default=60.0, help="hold per stage (s)")
    p.add_argument(
        "--warmup-s",
        type=float,
        default=10.0,
        help="settle time after scaling up before sampling (s)",
    )
    p.add_argument("--metrics-url", default="http://localhost:7006/metrics")
    p.add_argument(
        "--prometheus-url",
        default="",
        help="optional Prometheus server (e.g. http://localhost:9090) for cross-checks",
    )
    p.add_argument("--thresh-stt", type=float, default=2.0)
    p.add_argument("--thresh-llm", type=float, default=4.0)
    p.add_argument("--thresh-tts", type=float, default=1.5)
    p.add_argument("--thresh-tool-ratio", type=float, default=0.05)
    p.add_argument(
        "--skip-room-create",
        action="store_true",
        help="join rooms directly (join auto-creates on dev servers)",
    )
    args = p.parse_args(argv)
    args.ramp = [int(x) for x in args.ramp.split(",") if x.strip()]
    if not args.ramp:
        p.error("--ramp must contain at least one stage")
    return args


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        import livekit  # noqa: F401
        import livekit.api  # noqa: F401
    except ImportError:
        print(
            "voice_ramp requires the LiveKit SDKs against a live stack:\n"
            "  pip install livekit-api livekit\n"
            "and `make up-voice` running (see tests/load/README.md).",
            file=sys.stderr,
        )
        return 2
    started = time.perf_counter()
    code = asyncio.run(run_ramp(args))
    print(f"ramp finished in {time.perf_counter() - started:.0f}s (exit {code})")
    return code


if __name__ == "__main__":
    sys.exit(main())
