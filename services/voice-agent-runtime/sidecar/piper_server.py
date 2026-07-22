"""Piper HTTP sidecar: the TTS contract consumed by PiperTTS (mode=http).

POST /speak {"text": "...", "voice": "en_US-lessac-medium"} -> audio/wav
GET  /healthz

Voices are read from $PIPER_MODEL_DIR (default /voices) as
{voice}.onnx + {voice}.onnx.json. Download models at container start, e.g.:
  python -m piper.download_voices en_US-lessac-medium --download-dir /voices
(see README). Runs the `piper` CLI per request; dev-grade but robust across
piper-tts versions (no dependence on its Python API surface).
"""

from __future__ import annotations

import os
import subprocess
import tempfile

import uvicorn
from fastapi import FastAPI, HTTPException
from fastapi.responses import Response
from pydantic import BaseModel

PIPER_BIN = os.environ.get("PIPER_BIN", "piper")
MODEL_DIR = os.environ.get("PIPER_MODEL_DIR", "/voices")
DEFAULT_VOICE = os.environ.get("PIPER_VOICE", "en_US-lessac-medium")
PORT = int(os.environ.get("PORT", "5500"))
TIMEOUT_S = float(os.environ.get("PIPER_TIMEOUT_S", "30"))

app = FastAPI(title="opendesk-piper-sidecar")


class SpeakRequest(BaseModel):
    text: str
    voice: str | None = None


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok", "voice": DEFAULT_VOICE}


@app.post("/speak")
async def speak(req: SpeakRequest) -> Response:
    text = req.text.strip()
    if not text:
        raise HTTPException(status_code=400, detail="text must not be empty")
    voice = req.voice or DEFAULT_VOICE
    model = os.path.join(MODEL_DIR, f"{voice}.onnx")
    config = os.path.join(MODEL_DIR, f"{voice}.onnx.json")
    if not os.path.exists(model):
        raise HTTPException(
            status_code=404,
            detail=f"voice model not found: {model} (download it at container start; see README)",
        )

    with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as tmp:
        out_path = tmp.name
    try:
        proc = subprocess.run(
            [PIPER_BIN, "--model", model, "--config", config, "--output_file", out_path],
            input=text.encode("utf-8"),
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=TIMEOUT_S,
            check=False,
        )
        if proc.returncode != 0:
            raise HTTPException(
                status_code=502,
                detail=f"piper exited {proc.returncode}: {proc.stderr.decode(errors='replace')[:512]}",
            )
        with open(out_path, "rb") as fh:
            wav = fh.read()
    except subprocess.TimeoutExpired as exc:
        raise HTTPException(status_code=504, detail="piper timed out") from exc
    finally:
        try:
            os.unlink(out_path)
        except OSError:
            pass

    return Response(content=wav, media_type="audio/wav")


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=PORT, log_level="info")
