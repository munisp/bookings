"""conversation-service entrypoint (SPEC §7 conversation schema, §4 topics).

FastAPI + asyncpg + structlog. On startup: Postgres pool, transcript sink
(Fluvio/Kafka), Dapr client, transcript indexer task. Graceful shutdown via
the ASGI lifespan (uvicorn forwards SIGINT/SIGTERM).
"""

from __future__ import annotations

import contextlib
from collections.abc import AsyncIterator
from dataclasses import dataclass

import uvicorn
from fastapi import FastAPI
from fastapi.responses import JSONResponse

from .config import Config, load
from .dapr_client import DaprClient
from .db import Database
from .indexer import TranscriptIndexer
from .logging import get_logger, setup
from .routes import router
from .sinks import KafkaSink, TranscriptSink, build_sink


@dataclass
class State:
    cfg: Config
    db: Database
    dapr: DaprClient
    sink: TranscriptSink
    intel_sink: TranscriptSink
    indexer: TranscriptIndexer | None
    privacy: PrivacyEraseConsumer | None
    log: object


@contextlib.asynccontextmanager
async def lifespan(app: FastAPI) -> AsyncIterator[None]:
    setup()
    log = get_logger("conversation-service")
    cfg = load()

    db = Database(cfg)
    await db.connect()
    # SPEC-W3 §4 innovation 3: idempotent ALTER for enrichment columns.
    try:
        await db.ensure_intel_columns()
    except Exception as exc:
        log.error("intel column bootstrap failed; enrichment inserts will fail",
                  error=str(exc))
    # SPEC-W3 §3: idempotent ALTER + unique partial index for turn
    # Idempotency-Key dedupe.
    try:
        await db.ensure_turn_idempotency()
    except Exception as exc:
        log.error("idempotency bootstrap failed; keyed turn appends will 500",
                  error=str(exc))

    dapr = DaprClient(cfg.dapr_host, cfg.dapr_http_port, cfg.dapr_pubsub_name)

    sink = build_sink(cfg)
    try:
        await sink.start()
    except Exception as exc:
        # Transcript sink is a streaming optimization; Postgres is the source
        # of truth. Degrade to "log-only" rather than refusing to start.
        log.error("transcript sink start failed; turns will only hit Postgres+Dapr",
                  error=str(exc))
        sink = _NullSink()

    # Enriched turns (call intelligence) → opendesk.conversation.enriched.
    intel_sink: TranscriptSink = KafkaSink(cfg.kafka_brokers, cfg.enriched_topic)
    try:
        await intel_sink.start()
    except Exception as exc:
        log.error("enriched sink start failed; enrichment stays in Postgres",
                  error=str(exc))
        intel_sink = _NullSink()

    indexer: TranscriptIndexer | None = None
    if cfg.indexer_enabled:
        indexer = TranscriptIndexer(cfg, db)
        indexer.start()

    app.state.cfg = cfg
    app.state.db = db
    app.state.dapr = dapr
    app.state.sink = sink
    app.state.intel_sink = intel_sink
    app.state.log = log
    log.info("conversation-service started", port=cfg.port, sink=cfg.transcript_sink,
             intel_llm=cfg.intel_llm)

    try:
        yield
    finally:
        log.info("conversation-service shutting down")
        if indexer is not None:
            await indexer.stop()
        if privacy is not None:
            with contextlib.suppress(Exception):
                await privacy.stop()
        with contextlib.suppress(Exception):
            await sink.close()
        with contextlib.suppress(Exception):
            await intel_sink.close()
        with contextlib.suppress(Exception):
            await dapr.close()
        with contextlib.suppress(Exception):
            await db.close()


class _NullSink:
    async def publish(self, record: dict) -> None:
        return None

    async def close(self) -> None:
        return None


app = FastAPI(title="OpenDesk conversation-service", version="0.1.0", lifespan=lifespan)
app.include_router(router)


@app.get("/healthz")
async def healthz() -> JSONResponse:
    try:
        await app.state.db.ping()
    except Exception as exc:
        return JSONResponse(
            {"status": "unavailable", "error": str(exc)}, status_code=503
        )
    return JSONResponse({"status": "ok"})


def main() -> None:
    cfg = load()
    uvicorn.run(
        "app.main:app",
        host="0.0.0.0",
        port=cfg.port,
        log_config=None,  # structlog owns logging
        timeout_graceful_shutdown=15,
    )


if __name__ == "__main__":
    main()
