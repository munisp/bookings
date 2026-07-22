"""GET /v1/recommendations (SPEC-W3 §3, innovation 9).

Reads the latest revenue-intelligence rows per offering from the Iceberg
table `gold.reco_pricing` (written by infra/lakehouse/spark/jobs/
revenue_intelligence.py) via a pyiceberg scan. The gold table is small (one
row per tenant × offering per job run), so a full scan + in-Python "latest
per offering" reduction is appropriate. When the table does not exist yet
(no Spark run so far) the endpoint returns an empty list.
"""

from __future__ import annotations

from typing import Any

import structlog
from pyiceberg.exceptions import NoSuchTableError

from .config import Settings
from .iceberg_tables import load_rest_catalog

log = structlog.get_logger()

RECO_TABLE = "gold.reco_pricing"

_RECO_FIELDS = (
    "tenant_id",
    "offering_id",
    "computed_at",
    "bookings_30d",
    "net_revenue_cents_30d",
    "peak_hour",
    "peak_share",
    "no_show_rate",
    "suggested_peak_multiplier",
    "suggested_deposit_pct",
)


def fetch_recommendations(settings: Settings, tenant: str) -> list[dict[str, Any]]:
    """Latest reco_pricing row per offering for one tenant (empty if the
    table is absent). Blocking pyiceberg I/O — call via asyncio.to_thread."""
    catalog = load_rest_catalog(settings)
    try:
        table = catalog.load_table(RECO_TABLE)
    except NoSuchTableError:
        log.info("recommendations.table_absent", table=RECO_TABLE)
        return []

    rows = table.scan(selected_fields=_RECO_FIELDS).to_arrow().to_pylist()

    latest: dict[str, dict[str, Any]] = {}
    for row in rows:
        if row.get("tenant_id") != tenant:
            continue
        key = str(row.get("offering_id") or "unknown")
        cur = latest.get(key)
        if cur is None or (row.get("computed_at") or 0) > (cur.get("computed_at") or 0):
            latest[key] = row

    out: list[dict[str, Any]] = []
    for row in latest.values():
        out.append(
            {
                "offering_id": row.get("offering_id"),
                "computed_at": row["computed_at"].isoformat() if row.get("computed_at") else None,
                "bookings_30d": row.get("bookings_30d"),
                "net_revenue_cents_30d": row.get("net_revenue_cents_30d"),
                "peak_hour": row.get("peak_hour"),
                "peak_share": row.get("peak_share"),
                "no_show_rate": row.get("no_show_rate"),
                "suggested_peak_multiplier": row.get("suggested_peak_multiplier"),
                "suggested_deposit_pct": row.get("suggested_deposit_pct"),
            }
        )
    out.sort(key=lambda r: str(r["offering_id"]))
    return out
