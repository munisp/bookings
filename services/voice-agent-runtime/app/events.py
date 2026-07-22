"""CloudEvents 1.0 envelope helpers (SPEC §4).

Envelope: {specversion, id, source, type, subject, time, tenantid (ext), data}.
Note: the booking-service command consumer requires `tenantid` to be the
tenant **UUID** and `subject` to be the tenant **slug**.
"""

from __future__ import annotations

import uuid
from datetime import datetime, timezone
from typing import Any

SOURCE = "voice-agent-runtime"


def new_cloudevent(
    *,
    type_: str,
    subject: str,
    tenant_uuid: str,
    data: dict[str, Any],
    event_id: str | None = None,
) -> dict[str, Any]:
    eid = event_id or str(uuid.uuid4())
    return {
        "specversion": "1.0",
        "id": eid,
        "source": SOURCE,
        "type": type_,
        "subject": subject,
        "time": datetime.now(timezone.utc)
        .isoformat(timespec="milliseconds")
        .replace("+00:00", "Z"),
        "tenantid": tenant_uuid,
        "data": data,
    }
