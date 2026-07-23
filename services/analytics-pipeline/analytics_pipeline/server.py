"""FastAPI sidecar: GET /healthz (consumer lag per topic), GET /metrics,
GET /v1/recommendations (SPEC-W3 §3) and GET /v1/metering (Wave 5 #9)."""

from __future__ import annotations

import asyncio
from datetime import date
from typing import Any

import structlog
from fastapi import FastAPI, HTTPException, Query
from fastapi.responses import JSONResponse, PlainTextResponse
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest

from .config import Settings
from .consumer import BronzeConsumer
from .metering import fetch_usage
from .recommendations import fetch_recommendations

log = structlog.get_logger()


def _parse_date(raw: str | None, param: str) -> date | None:
    if raw in (None, ""):
        return None
    try:
        return date.fromisoformat(raw)
    except ValueError:
        raise HTTPException(
            status_code=400, detail=f"{param} must be an ISO date (YYYY-MM-DD)"
        ) from None


def create_app(
    consumer: BronzeConsumer, ready_flag: dict[str, bool], settings: Settings
) -> FastAPI:
    app = FastAPI(title="opendesk-analytics-pipeline", version="0.1.0")

    @app.get("/healthz")
    async def healthz() -> JSONResponse:
        lag = await consumer.lag_report()
        ready = ready_flag.get("ready", False) and consumer.running
        body: dict[str, Any] = {
            "status": "ok" if ready else "starting",
            "consumer_running": consumer.running,
            "last_error": consumer.last_error,
            "topics": lag,
        }
        # 503 while still starting so compose healthchecks gate dependents;
        # flush errors alone do not fail health (retry loop is by design).
        return JSONResponse(body, status_code=200 if ready else 503)

    @app.get("/metrics")
    async def metrics_endpoint() -> PlainTextResponse:
        return PlainTextResponse(generate_latest().decode("utf-8"),
                                 media_type=CONTENT_TYPE_LATEST)

    @app.get("/v1/recommendations")
    async def recommendations(tenant: str = Query(min_length=1)) -> dict[str, Any]:
        """Latest pricing recommendations per offering for a tenant
        (SPEC-W3 §3 innovation 9). `tenant` is the tenant UUID as stored in
        the lakehouse. Empty list when gold.reco_pricing does not exist yet."""
        try:
            items = await asyncio.to_thread(fetch_recommendations, settings, tenant)
        except Exception as exc:  # noqa: BLE001 — iceberg/MinIO outage
            log.warning("recommendations.failed", error=str(exc))
            raise HTTPException(status_code=502, detail=f"lakehouse error: {exc}") from exc
        return {"tenant": tenant, "recommendations": items}

    @app.get("/v1/metering")
    async def metering(
        tenant: str = Query(min_length=1),
        from_: str | None = Query(default=None, alias="from"),
        to: str | None = None,
    ) -> dict[str, Any]:
        """Aggregated usage for a tenant (Wave 5 #9): rows of
        {tenant_id, date, metric, total_value} over bronze.usage_events,
        optionally bounded by [from, to] ISO dates (inclusive). Empty list
        when no usage exists yet — sparse data is normal in v1."""
        date_from = _parse_date(from_, "from")
        date_to = _parse_date(to, "to")
        if date_from is not None and date_to is not None and date_from > date_to:
            raise HTTPException(status_code=400, detail="from must be <= to")
        try:
            items = await asyncio.to_thread(
                fetch_usage, settings, tenant, date_from, date_to
            )
        except Exception as exc:  # noqa: BLE001 — iceberg/MinIO outage
            log.warning("metering.failed", error=str(exc))
            raise HTTPException(status_code=502, detail=f"lakehouse error: {exc}") from exc
        return {"tenant": tenant, "usage": items}

    return app
