"""Raw transcript sinks (SPEC §5).

Every accepted turn is streamed as a raw record
{conversationId, tenantId, role, text, ts} to topic
`opendesk.transcripts-raw` for high-throughput edge/telephony ingestion
(PII redaction happens downstream in the Fluvio smart module).

Two implementations behind the TranscriptSink protocol, selected by env
TRANSCRIPT_SINK=fluvio|kafka (default kafka):
  - FluvioSink  — official fluvio python client (import guarded; the sync
                  client is invoked via asyncio.to_thread)
  - KafkaSink   — aiokafka producer fallback writing the same topic on Kafka
"""

from __future__ import annotations

import asyncio
import json
from typing import Any, Protocol

from .config import Config
from .logging import get_logger

log = get_logger(__name__)


class TranscriptSink(Protocol):
    async def start(self) -> None: ...
    async def publish(self, record: dict[str, Any]) -> None: ...
    async def close(self) -> None: ...


class FluvioSink:
    """Publishes raw transcripts via the fluvio python client.

    The import is guarded so the service runs without the optional
    `fluvio` package; construction then raises RuntimeError and the
    factory falls back to KafkaSink.
    """

    def __init__(self, topic: str) -> None:
        try:
            from fluvio import Fluvio  # type: ignore[import-not-found]
        except ImportError as exc:  # pragma: no cover - env dependent
            raise RuntimeError(
                "TRANSCRIPT_SINK=fluvio requires the optional 'fluvio' package"
            ) from exc
        self._topic = topic
        self._fluvio_mod = Fluvio
        self._producer: Any = None

    async def start(self) -> None:
        def _connect() -> Any:
            client = self._fluvio_mod.connect()
            return client.topic_producer(self._topic)

        self._producer = await asyncio.to_thread(_connect)
        log.info("fluvio sink connected", topic=self._topic)

    async def publish(self, record: dict[str, Any]) -> None:
        assert self._producer is not None, "FluvioSink.start() not called"
        payload = json.dumps(record)

        def _send() -> None:
            self._producer.send_string(payload)

        await asyncio.to_thread(_send)

    async def close(self) -> None:
        self._producer = None


class KafkaSink:
    """aiokafka fallback sink for the raw transcript topic."""

    def __init__(self, brokers: list[str], topic: str) -> None:
        self._brokers = brokers
        self._topic = topic
        self._producer: Any = None

    async def start(self) -> None:
        from aiokafka import AIOKafkaProducer

        # construct inside the running loop (aiokafka requirement)
        self._producer = AIOKafkaProducer(bootstrap_servers=self._brokers)
        await self._producer.start()
        log.info("kafka sink connected", topic=self._topic)

    async def publish(self, record: dict[str, Any]) -> None:
        assert self._producer is not None, "KafkaSink.start() not called"
        key = str(record.get("conversationId", "")).encode()
        await self._producer.send_and_wait(
            self._topic, json.dumps(record).encode(), key=key or None
        )

    async def close(self) -> None:
        if self._producer is not None:
            await self._producer.stop()
            self._producer = None


def build_sink(cfg: Config) -> TranscriptSink:
    """Select the sink per TRANSCRIPT_SINK (default kafka).

    Falls back to KafkaSink when the fluvio client is unavailable.
    """
    if cfg.transcript_sink == "fluvio":
        try:
            return FluvioSink(cfg.fluvio_topic)
        except RuntimeError as exc:
            log.warning("fluvio sink unavailable, falling back to kafka", error=str(exc))
    return KafkaSink(cfg.kafka_brokers, cfg.fluvio_topic)
