"""FastAPI sidecar: GET /healthz (consumer lag per topic) and GET /metrics."""

from __future__ import annotations

from typing import Any

import structlog
from fastapi import FastAPI
from fastapi.responses import JSONResponse, PlainTextResponse
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest

from .consumer import BronzeConsumer

log = structlog.get_logger()


def create_app(consumer: BronzeConsumer, ready_flag: dict[str, bool]) -> FastAPI:
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

    return app
