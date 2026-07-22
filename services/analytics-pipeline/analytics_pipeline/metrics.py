"""Prometheus metrics for the sink (exposed by the sidecar on GET /metrics)."""

from __future__ import annotations

from prometheus_client import Counter, Gauge, Histogram

MESSAGES_CONSUMED = Counter(
    "analytics_messages_consumed_total",
    "Kafka messages consumed and buffered.",
    ["topic"],
)
ROWS_WRITTEN = Counter(
    "analytics_rows_written_total",
    "Rows successfully appended to Iceberg bronze tables.",
    ["table"],
)
FLUSHES = Counter(
    "analytics_flushes_total",
    "Micro-batch flush attempts by outcome.",
    ["table", "outcome"],  # ok | error
)
FLUSH_DURATION = Histogram(
    "analytics_flush_duration_seconds",
    "Iceberg append latency per micro-batch.",
    ["table"],
    buckets=(0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0),
)
BUFFER_SIZE = Gauge(
    "analytics_buffer_messages",
    "Messages currently buffered per topic awaiting flush.",
    ["topic"],
)
CONSUMER_LAG = Gauge(
    "analytics_consumer_lag",
    "Approximate consumer lag (highwater - position) per topic/partition.",
    ["topic", "partition"],
)
CONSUMER_UP = Gauge(
    "analytics_consumer_running",
    "1 when the Kafka consumer loop is running.",
)
