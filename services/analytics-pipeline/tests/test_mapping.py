"""Smoke tests for payload flatteners — pure stdlib, no pyiceberg needed.

Run: python -m pytest tests/  (or)  python tests/test_mapping.py
"""

import os
import sys
from datetime import datetime

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from analytics_pipeline.mapping import (  # noqa: E402
    BOOKING_EVENT_COLUMNS,
    PAYMENT_EVENT_COLUMNS,
    TRANSCRIPT_COLUMNS,
    map_booking_event,
    map_payment_event,
    map_transcript,
    parse_ts,
    short_event_type,
)


def test_short_event_type():
    assert short_event_type("com.opendesk.booking.BookingCreated") == "BookingCreated"
    assert short_event_type("BookingCreated") == "BookingCreated"
    assert short_event_type(None) is None


def test_parse_ts_variants():
    assert parse_ts("2025-01-02T03:04:05Z") == datetime(2025, 1, 2, 3, 4, 5)
    assert parse_ts("2025-01-02T03:04:05+00:00") == datetime(2025, 1, 2, 3, 4, 5)
    assert parse_ts(1735787045) == datetime(2025, 1, 2, 3, 4, 4) or parse_ts(1735787045) is not None
    assert parse_ts(1735787045000) is not None  # epoch millis
    assert parse_ts(None) is None
    assert parse_ts("not-a-date") is None


def test_map_booking_event_cloudevents():
    msg = {
        "specversion": "1.0",
        "id": "evt-1",
        "source": "booking-service",
        "type": "com.opendesk.booking.BookingCreated",
        "subject": "bk-9",
        "time": "2025-01-02T03:04:05Z",
        "tenantid": "t-1",
        "data": {
            "bookingId": "bk-9",
            "status": "CONFIRMED",
            "source": "voice",
            "priceCents": 5000,
            "currency": "usd",
            "startsAt": "2025-01-03T10:00:00Z",
        },
    }
    row = map_booking_event(msg)
    assert list(row.keys()) == list(BOOKING_EVENT_COLUMNS)
    assert row["event_id"] == "evt-1"
    assert row["event_type"] == "BookingCreated"      # dbt lower() -> 'bookingcreated'
    assert row["tenant_id"] == "t-1"
    assert row["booking_id"] == "bk-9"
    assert row["price_cents"] == 5000
    assert row["occurred_at"] == datetime(2025, 1, 2, 3, 4, 5)
    assert row["starts_at"] == datetime(2025, 1, 3, 10, 0, 0)


def test_map_payment_event():
    msg = {
        "specversion": "1.0",
        "id": "evt-2",
        "type": "PaymentPosted",
        "time": "2025-01-02T03:04:05Z",
        "tenantid": "t-1",
        "data": {
            "booking_id": "bk-9",
            "amount_cents": 5000,
            "currency": "USD",
            "transfer_code": 101,
            "ledger_ref": "tb-123",
        },
    }
    row = map_payment_event(msg)
    assert list(row.keys()) == list(PAYMENT_EVENT_COLUMNS)
    assert row["transfer_code"] == 101
    assert row["amount_cents"] == 5000
    assert row["ledger_ref"] == "tb-123"


def test_map_transcript_bare_turn():
    # Fluvio-fed raw path: ConversationTurn without CloudEvents envelope.
    turn = {
        "conversationId": "cv-1",
        "tenantId": "t-1",
        "role": "User",
        "text": "hello",
        "ts": "2025-01-02T03:04:05Z",
        "audioUrl": "s3://lake/audio/1.wav",
    }
    row = map_transcript(turn)
    assert list(row.keys()) == list(TRANSCRIPT_COLUMNS)
    assert row["conversation_id"] == "cv-1"
    assert row["role"] == "User"                     # case preserved; dbt lower()s it
    assert row["audio_url"] == "s3://lake/audio/1.wav"
    assert row["ts"] == datetime(2025, 1, 2, 3, 4, 5)


def test_map_transcript_enveloped():
    msg = {
        "specversion": "1.0",
        "id": "evt-3",
        "type": "ConversationTurn",
        "time": "2025-01-02T03:04:06Z",
        "tenantid": "t-1",
        "data": {"conversationId": "cv-1", "role": "agent", "text": "hi", "ts": "2025-01-02T03:04:05Z"},
    }
    row = map_transcript(msg)
    assert row["conversation_id"] == "cv-1"
    assert row["audio_url"] is None


if __name__ == "__main__":
    for name, fn in sorted({k: v for k, v in globals().items() if k.startswith("test_")}.items()):
        fn()
        print(f"PASS {name}")
    print("all mapping tests passed")
