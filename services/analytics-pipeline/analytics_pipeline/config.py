"""Environment-driven settings (no pydantic — stdlib dataclass).

Every knob is documented in the README env table; defaults target the dev compose
network (`opendesk`) from infra/docker-compose.lakehouse.yml + the core compose.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field


def _int(name: str, default: int) -> int:
    raw = os.getenv(name)
    if raw is None or raw == "":
        return default
    return int(raw)


def _bool(name: str, default: bool) -> bool:
    raw = os.getenv(name)
    if raw is None or raw == "":
        return default
    return raw.strip().lower() in {"1", "true", "yes", "on"}


@dataclass(frozen=True)
class Settings:
    # Kafka (SPEC §4 topics)
    kafka_bootstrap_servers: str = field(
        default_factory=lambda: os.getenv("KAFKA_BOOTSTRAP_SERVERS", "kafka:9092")
    )
    kafka_group_id: str = field(
        default_factory=lambda: os.getenv("KAFKA_GROUP_ID", "analytics-pipeline")
    )
    topic_booking_events: str = field(
        default_factory=lambda: os.getenv("TOPIC_BOOKING_EVENTS", "opendesk.booking.events")
    )
    topic_payment_events: str = field(
        default_factory=lambda: os.getenv("TOPIC_PAYMENT_EVENTS", "opendesk.payments.events")
    )
    topic_transcripts: str = field(
        default_factory=lambda: os.getenv("TOPIC_TRANSCRIPTS", "opendesk.conversation.transcripts")
    )

    # Micro-batching
    batch_size: int = field(default_factory=lambda: _int("BATCH_SIZE", 500))
    flush_interval_seconds: float = field(
        default_factory=lambda: float(os.getenv("FLUSH_INTERVAL", "15"))
    )

    # Iceberg REST catalog + MinIO warehouse (SPEC §13)
    iceberg_rest_uri: str = field(
        default_factory=lambda: os.getenv("ICEBERG_REST_URI", "http://iceberg-rest:8181")
    )
    iceberg_warehouse: str = field(
        default_factory=lambda: os.getenv("ICEBERG_WAREHOUSE", "s3://lake/warehouse")
    )
    aws_access_key_id: str = field(
        default_factory=lambda: os.getenv("AWS_ACCESS_KEY_ID", "minioadmin")
    )
    aws_secret_access_key: str = field(
        default_factory=lambda: os.getenv("AWS_SECRET_ACCESS_KEY", "minioadmin")
    )
    aws_endpoint_url: str = field(
        default_factory=lambda: os.getenv("AWS_ENDPOINT_URL", "http://minio:9000")
    )
    aws_region: str = field(default_factory=lambda: os.getenv("AWS_REGION", "us-east-1"))
    auto_create_tables: bool = field(
        default_factory=lambda: _bool("AUTO_CREATE_TABLES", True)
    )

    # Sidecar HTTP server
    port: int = field(default_factory=lambda: _int("PORT", 7009))
    host: str = field(default_factory=lambda: os.getenv("HOST", "0.0.0.0"))

    # Startup resilience: catalog/kafka may still be booting in compose.
    startup_retry_seconds: float = field(
        default_factory=lambda: float(os.getenv("STARTUP_RETRY_SECONDS", "5"))
    )
    startup_max_attempts: int = field(default_factory=lambda: _int("STARTUP_MAX_ATTEMPTS", 60))


def load_settings() -> Settings:
    return Settings()
