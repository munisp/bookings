"""Entrypoint: bootstrap Iceberg, start Kafka consumer + sidecar HTTP server.

Startup is retry-loop resilient: in the dev compose the Iceberg REST catalog and
Kafka may still be booting when this container starts.
"""

from __future__ import annotations

import asyncio
import signal

import structlog
import uvicorn

from .config import load_settings
from .consumer import BronzeConsumer
from .iceberg_tables import IcebergSink, ensure_bronze, load_rest_catalog
from .server import create_app

log = structlog.get_logger()


def configure_logging() -> None:
    structlog.configure(
        processors=[
            structlog.contextvars.merge_contextvars,
            structlog.processors.add_log_level,
            structlog.processors.TimeStamper(fmt="iso", utc=True),
            structlog.processors.StackInfoRenderer(),
            structlog.processors.format_exc_info,
            structlog.processors.JSONRenderer(),
        ],
        wrapper_class=structlog.make_filtering_bound_logger(0),
        cache_logger_on_first_use=True,
    )


async def _with_retry(fn, what: str, settings) -> None:
    for attempt in range(1, settings.startup_max_attempts + 1):
        try:
            await fn()
            return
        except Exception as exc:
            log.warning(
                "startup.retry",
                dependency=what,
                attempt=attempt,
                error=f"{type(exc).__name__}: {exc}",
            )
            await asyncio.sleep(settings.startup_retry_seconds)
    raise RuntimeError(f"{what} not reachable after {settings.startup_max_attempts} attempts")


async def amain() -> None:
    configure_logging()
    settings = load_settings()
    log.info("startup.settings", **{
        "kafka": settings.kafka_bootstrap_servers,
        "group": settings.kafka_group_id,
        "batch_size": settings.batch_size,
        "flush_interval": settings.flush_interval_seconds,
        "iceberg_rest": settings.iceberg_rest_uri,
        "warehouse": settings.iceberg_warehouse,
        "s3_endpoint": settings.aws_endpoint_url,
        "port": settings.port,
    })

    # Iceberg catalog + bronze tables (blocking client calls -> thread).
    catalog = load_rest_catalog(settings)
    if settings.auto_create_tables:
        async def _ensure():
            await asyncio.to_thread(ensure_bronze, catalog)
        await _with_retry(_ensure, "iceberg-rest", settings)
        log.info("iceberg.bronze_ready")

    sink = IcebergSink(catalog)
    consumer = BronzeConsumer(settings, sink)
    await _with_retry(consumer.start, "kafka", settings)

    ready_flag = {"ready": True}
    app = create_app(consumer, ready_flag, settings)
    server = uvicorn.Server(
        uvicorn.Config(app, host=settings.host, port=settings.port,
                       log_level="info", access_log=False)
    )

    stop_event = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, stop_event.set)

    consumer_task = asyncio.create_task(consumer.run(stop_event))
    server_task = asyncio.create_task(server.serve())

    await stop_event.wait()
    log.info("shutdown.requested")
    ready_flag["ready"] = False
    server.should_exit = True
    await consumer.stop()
    await asyncio.gather(consumer_task, server_task, return_exceptions=True)
    log.info("shutdown.complete")


def main() -> None:
    try:
        asyncio.run(amain())
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
