"""TTS stage: interface + Piper implementation.

Two modes (env PIPER_MODE):
- `http` (default): POST {PIPER_HTTP_URL}/speak {"text", "voice"} expecting
  audio/wav back. The companion sidecar in ./sidecar implements exactly this
  contract (see docker-compose.fragment.yml).
- `subprocess`: runs the local `piper` binary (env PIPER_BIN) with the voice
  model from PIPER_MODEL_DIR (env PIPER_VOICE, e.g. en_US-lessac-medium);
  see README for model download.

Both return signed-16-bit mono PCM at `sample_rate` (default 22050 Hz).
"""

from __future__ import annotations

import asyncio
import io
import os
import tempfile
import wave
from typing import Protocol

import httpx

from ..logging import get_logger

log = get_logger("tts")


class TTSInterface(Protocol):
    sample_rate: int

    async def synthesize_pcm(self, text: str) -> bytes:
        """Synthesize text to signed-16-bit mono PCM at `sample_rate`."""
        ...


def _wav_to_pcm(wav_bytes: bytes) -> tuple[bytes, int]:
    with wave.open(io.BytesIO(wav_bytes), "rb") as wf:
        channels = wf.getnchannels()
        sampwidth = wf.getsampwidth()
        rate = wf.getframerate()
        frames = wf.readframes(wf.getnframes())
    if sampwidth != 2:
        raise RuntimeError(f"unsupported piper wav sample width: {sampwidth}")
    if channels != 1:
        # Downmix interleaved channels by simple averaging.
        import numpy as np

        audio = np.frombuffer(frames, dtype=np.int16)
        audio = audio.reshape(-1, channels).mean(axis=1).astype(np.int16)
        frames = audio.tobytes()
    return frames, rate


class PiperTTS:
    def __init__(
        self,
        *,
        mode: str = "http",
        http_url: str = "http://piper:5500",
        voice: str = "en_US-lessac-medium",
        piper_bin: str = "piper",
        model_dir: str = "/voices",
        sample_rate: int = 22050,
        timeout_s: float = 30.0,
    ) -> None:
        self.mode = mode
        self.http_url = http_url.rstrip("/")
        self.voice = voice
        self.piper_bin = piper_bin
        self.model_dir = model_dir
        self.sample_rate = sample_rate
        self._timeout = timeout_s
        self._client: httpx.AsyncClient | None = None

    async def aclose(self) -> None:
        if self._client is not None:
            await self._client.aclose()
            self._client = None

    def _http(self) -> httpx.AsyncClient:
        if self._client is None:
            self._client = httpx.AsyncClient(timeout=httpx.Timeout(self._timeout))
        return self._client

    async def synthesize_pcm(self, text: str) -> bytes:
        text = text.strip()
        if not text:
            return b""
        if self.mode == "subprocess":
            pcm, rate = await self._synthesize_subprocess(text)
        else:
            pcm, rate = await self._synthesize_http(text)
        if rate != self.sample_rate:
            log.warning(
                "piper sample rate mismatch; audio may be pitched",
                expected=self.sample_rate,
                got=rate,
            )
        return pcm

    async def _synthesize_http(self, text: str) -> tuple[bytes, int]:
        resp = await self._http().post(
            f"{self.http_url}/speak", json={"text": text, "voice": self.voice}
        )
        resp.raise_for_status()
        return _wav_to_pcm(resp.content)

    async def _synthesize_subprocess(self, text: str) -> tuple[bytes, int]:
        model = os.path.join(self.model_dir, f"{self.voice}.onnx")
        config = os.path.join(self.model_dir, f"{self.voice}.onnx.json")

        # Run the blocking subprocess in a thread to keep the event loop free.
        def _exec() -> bytes:
            import subprocess

            with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as tmp:
                out_path = tmp.name
            try:
                cmd = [
                    self.piper_bin,
                    "--model",
                    model,
                    "--config",
                    config,
                    "--output_file",
                    out_path,
                ]
                proc = subprocess.run(
                    cmd,
                    input=text.encode("utf-8"),
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    timeout=self._timeout,
                    check=False,
                )
                if proc.returncode != 0:
                    raise RuntimeError(
                        f"piper exited {proc.returncode}: {proc.stderr.decode(errors='replace')[:512]}"
                    )
                with open(out_path, "rb") as fh:
                    return fh.read()
            finally:
                try:
                    os.unlink(out_path)
                except OSError:
                    pass

        wav_bytes = await asyncio.to_thread(_exec)
        return _wav_to_pcm(wav_bytes)
