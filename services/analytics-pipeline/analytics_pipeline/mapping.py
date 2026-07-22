"""Pure-python flattening of Kafka payloads into bronze table rows.

Column lists below are the CONTRACT with the dbt project
(infra/lakehouse/dbt/models/silver/schema.yml, sources.bronze) — do not rename
or reorder without changing both sides.

Input payloads (SPEC §4):
- CloudEvents 1.0 envelope everywhere: {specversion, id, source, type, subject,
  time, tenantid (ext), data}. ``type`` may be a short name ("BookingCreated")
  or a reverse-DNS name ("com.opendesk.booking.BookingCreated") — we keep the
  last segment so dbt's lower(event_type) comparisons match.
- ``opendesk.conversation.transcripts`` carries ConversationTurn
  {conversationId, tenantId, role, text, ts, audioUrl?} as the CE ``data``
  payload; bare (non-enveloped) turn messages are tolerated for the raw
  Fluvio-fed path.

All timestamps are normalised to *naive UTC* datetimes (Iceberg ``timestamp``
without timezone — consistent with the Spark silver jobs, which run with
``spark.sql.iceberg.handle-timestamp-without-timezone=true``).
"""

from __future__ import annotations

import re
from datetime import datetime, timezone
from typing import Any, Mapping, Optional

# --- canonical bronze columns (order = pyiceberg/pyarrow schema order) --------
BOOKING_EVENT_COLUMNS = (
    "event_id",
    "event_type",
    "tenant_id",
    "booking_id",
    "status",
    "source",
    "price_cents",
    "currency",
    "starts_at",
    "occurred_at",
    # SPEC-W3 §3 innovation 9: revenue intelligence needs per-offering
    # granularity; appended at the END to keep pyiceberg field ids stable.
    "offering_id",
)

PAYMENT_EVENT_COLUMNS = (
    "event_id",
    "event_type",
    "tenant_id",
    "booking_id",
    "amount_cents",
    "currency",
    "transfer_code",
    "ledger_ref",
    "occurred_at",
)

TRANSCRIPT_COLUMNS = (
    "conversation_id",
    "tenant_id",
    "role",
    "text",
    "ts",
    "audio_url",
)

_CAMEL_RE = re.compile(r"(?<!^)(?=[A-Z])")


def _snake(name: str) -> str:
    return _CAMEL_RE.sub("_", name).lower()


def _get(data: Mapping[str, Any], *keys: str) -> Optional[Any]:
    """Fetch the first present key, tolerating camelCase/snake_case variants."""
    for key in keys:
        for variant in {key, _snake(key)}:
            if variant in data and data[variant] is not None:
                return data[variant]
    return None


def split_envelope(message: Mapping[str, Any]) -> tuple[Mapping[str, Any], Mapping[str, Any]]:
    """Return (envelope, data). Bare payloads yield an empty envelope."""
    if "specversion" in message:
        data = message.get("data")
        return message, data if isinstance(data, Mapping) else {}
    return {}, message


def short_event_type(raw_type: Optional[str]) -> Optional[str]:
    """'com.opendesk.booking.BookingCreated' -> 'BookingCreated'."""
    if not raw_type:
        return None
    return raw_type.rsplit(".", 1)[-1]


def parse_ts(value: Any) -> Optional[datetime]:
    """Parse ISO-8601 strings, epoch seconds, or epoch millis -> naive UTC."""
    if value is None:
        return None
    if isinstance(value, datetime):
        dt = value
    elif isinstance(value, (int, float)):
        # Heuristic: values beyond ~ year 3366 in seconds are millis.
        seconds = value / 1000.0 if value > 1e11 else float(value)
        dt = datetime.fromtimestamp(seconds, tz=timezone.utc)
    elif isinstance(value, str):
        text = value.strip()
        if text.endswith("Z"):
            text = text[:-1] + "+00:00"
        try:
            dt = datetime.fromisoformat(text)
        except ValueError:
            return None
    else:
        return None
    if dt.tzinfo is not None:
        dt = dt.astimezone(timezone.utc).replace(tzinfo=None)
    return dt.replace(tzinfo=None)


def _as_int(value: Any) -> Optional[int]:
    if value is None or isinstance(value, bool):
        return None
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def _as_str(value: Any) -> Optional[str]:
    if value is None:
        return None
    return value if isinstance(value, str) else str(value)


def map_booking_event(message: Mapping[str, Any]) -> dict[str, Any]:
    envelope, data = split_envelope(message)
    return {
        "event_id": _as_str(envelope.get("id") or _get(data, "event_id", "eventId")),
        "event_type": _as_str(
            short_event_type(envelope.get("type")) or _get(data, "event_type", "eventType")
        ),
        "tenant_id": _as_str(
            envelope.get("tenantid") or _get(data, "tenant_id", "tenantId")
        ),
        "booking_id": _as_str(
            _get(data, "booking_id", "bookingId") or envelope.get("subject")
        ),
        "status": _as_str(_get(data, "status")),
        "source": _as_str(_get(data, "source", "channel")),
        "price_cents": _as_int(_get(data, "price_cents", "priceCents")),
        "currency": _as_str(_get(data, "currency")),
        "starts_at": parse_ts(_get(data, "starts_at", "startsAt")),
        "occurred_at": parse_ts(envelope.get("time"))
        or parse_ts(_get(data, "occurred_at", "occurredAt")),
        "offering_id": _as_str(_get(data, "offering_id", "offeringId")),
    }


def map_payment_event(message: Mapping[str, Any]) -> dict[str, Any]:
    envelope, data = split_envelope(message)
    return {
        "event_id": _as_str(envelope.get("id") or _get(data, "event_id", "eventId")),
        "event_type": _as_str(
            short_event_type(envelope.get("type")) or _get(data, "event_type", "eventType")
        ),
        "tenant_id": _as_str(
            envelope.get("tenantid") or _get(data, "tenant_id", "tenantId")
        ),
        "booking_id": _as_str(
            _get(data, "booking_id", "bookingId") or envelope.get("subject")
        ),
        "amount_cents": _as_int(_get(data, "amount_cents", "amountCents")),
        "currency": _as_str(_get(data, "currency")),
        "transfer_code": _as_int(_get(data, "transfer_code", "transferCode", "code")),
        "ledger_ref": _as_str(_get(data, "ledger_ref", "ledgerRef")),
        "occurred_at": parse_ts(envelope.get("time"))
        or parse_ts(_get(data, "occurred_at", "occurredAt")),
    }


def map_transcript(message: Mapping[str, Any]) -> dict[str, Any]:
    envelope, data = split_envelope(message)
    return {
        "conversation_id": _as_str(
            _get(data, "conversation_id", "conversationId") or envelope.get("subject")
        ),
        "tenant_id": _as_str(
            envelope.get("tenantid") or _get(data, "tenant_id", "tenantId")
        ),
        "role": _as_str(_get(data, "role")),
        "text": _as_str(_get(data, "text")),
        "ts": parse_ts(_get(data, "ts", "timestamp")) or parse_ts(envelope.get("time")),
        "audio_url": _as_str(_get(data, "audio_url", "audioUrl")),
    }
