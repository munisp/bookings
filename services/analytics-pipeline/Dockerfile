# syntax=docker/dockerfile:1

# --- build stage: resolve + install pinned deps into a venv -------------------
FROM python:3.12-slim AS builder
ENV PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1
WORKDIR /build
RUN python -m venv /opt/venv
ENV PATH="/opt/venv/bin:$PATH"
RUN pip install --upgrade "pip==24.3.1"
COPY pyproject.toml README.md ./
COPY analytics_pipeline ./analytics_pipeline
RUN pip install .

# --- runtime stage -------------------------------------------------------------
FROM python:3.12-slim
ENV PATH="/opt/venv/bin:$PATH" \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1
RUN groupadd --system app && useradd --system --gid app app
COPY --from=builder /opt/venv /opt/venv
USER app
EXPOSE 7009
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=6 \
  CMD ["python", "-c", "import urllib.request,sys; r=urllib.request.urlopen('http://127.0.0.1:7009/healthz', timeout=4); sys.exit(0 if r.status==200 else 1)"]
CMD ["python", "-m", "analytics_pipeline.main"]
