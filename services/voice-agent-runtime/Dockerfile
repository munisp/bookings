# syntax=docker/dockerfile:1
# Multi-stage build: python:3.12-slim (SPEC §15).

FROM python:3.12-slim AS build
WORKDIR /app

# Build deps for ctranslate2/faster-whisper wheels (most ship manylinux
# wheels; build-essential is a safety net).
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential \
    && rm -rf /var/lib/apt/lists/*

COPY pyproject.toml README.md ./
COPY app ./app
RUN pip wheel --no-cache-dir --wheel-dir /wheels .

FROM python:3.12-slim
RUN useradd -r -u 10001 opendesk \
    && mkdir -p /models \
    && chown opendesk /models
WORKDIR /app

COPY --from=build /wheels /wheels
RUN pip install --no-cache-dir /wheels/*.whl \
    && rm -rf /wheels

USER opendesk
EXPOSE 7006

# Model downloads are RUNTIME/INIT steps (not baked into the image):
# - faster-whisper downloads WHISPER_MODEL from HuggingFace on first
#   transcription; mount /models as a volume and set HF_HOME=/models to cache.
# - piper voice models live in the piper sidecar (see sidecar/Dockerfile and
#   docker-compose.fragment.yml `piper-init`).
ENV PORT=7006 \
    LOG_LEVEL=info \
    HF_HOME=/models \
    DAPR_HOST=daprd-voice \
    DAPR_HTTP_PORT=3500 \
    LLM_BASE_URL=http://ollama:11434/v1 \
    LLM_MODEL=llama3.1:8b \
    WHISPER_MODEL=base \
    PIPER_MODE=http \
    PIPER_HTTP_URL=http://piper:5500 \
    AGENT_BACKEND=livekit

# Default: FastAPI control plane on 7006. The LiveKit Agents worker runs as a
# separate process in the same image:
#   docker run ... voice-agent-runtime python -m app.livekit_worker start
ENTRYPOINT ["python", "-m", "app.main"]
