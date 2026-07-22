//! WebSocket endpoints (SPEC §12: `/ws/*` routed here via APISIX).
//!
//! - `GET /ws?tenant={slug}&token={jwt}` — live booking events for the tenant
//!   (Kafka `opendesk.booking.events`).
//! - `GET /ws/transcripts?tenant={slug}&token={jwt}` — live transcript tail
//!   (Fluvio `opendesk.transcripts-raw`).
//! - `GET /ws/intel?tenant={slug}&token={jwt}` — live enriched turns
//!   (sentiment/intent/entities; Kafka `opendesk.conversation.enriched`).
//!
//! Backpressure: drop-slow policy — consumers lagging past the channel
//! capacity get a `{"type":"lagged","dropped":n}` notice and the drop is
//! counted in `/metrics`.

use std::sync::Arc;

use axum::{
    extract::{
        ws::{Message, WebSocket, WebSocketUpgrade},
        Query, State,
    },
    http::StatusCode,
    response::{IntoResponse, Response},
};
use serde::Deserialize;
use tokio::sync::broadcast;
use tracing::{debug, warn};

use crate::auth::AuthError;
use crate::bus;
use crate::metrics;
use crate::AppState;

#[derive(Debug, Deserialize)]
pub struct WsQuery {
    pub tenant: String,
    pub token: Option<String>,
}

async fn authorize(state: &AppState, query: &WsQuery) -> Result<(), Response> {
    match state
        .auth
        .authenticate(query.token.as_deref(), &query.tenant)
        .await
    {
        Ok(_) => Ok(()),
        Err(e) => {
            metrics::inc(&metrics::AUTH_FAILURES);
            let status = match &e {
                AuthError::MissingToken | AuthError::MalformedToken => StatusCode::UNAUTHORIZED,
                AuthError::Validation(_) => StatusCode::UNAUTHORIZED,
                AuthError::Forbidden(_) => StatusCode::FORBIDDEN,
                AuthError::JwksFetch(_) => StatusCode::BAD_GATEWAY,
            };
            warn!(error = %e, tenant = %query.tenant, "ws auth failed");
            Err((
                status,
                axum::Json(serde_json::json!({ "error": e.to_string() })),
            )
                .into_response())
        }
    }
}

pub async fn ws_booking_events(
    State(state): State<AppState>,
    Query(query): Query<WsQuery>,
    ws: WebSocketUpgrade,
) -> Response {
    if let Err(resp) = authorize(&state, &query).await {
        return resp;
    }
    let channel = bus::booking_channel(&query.tenant);
    let rx = state.bus.subscribe(&channel).await;
    debug!(tenant = %query.tenant, "booking events subscriber connected");
    ws.on_upgrade(move |socket| handle_socket(socket, rx))
}

pub async fn ws_transcripts(
    State(state): State<AppState>,
    Query(query): Query<WsQuery>,
    ws: WebSocketUpgrade,
) -> Response {
    if let Err(resp) = authorize(&state, &query).await {
        return resp;
    }
    let channel = bus::transcripts_channel(&query.tenant);
    let rx = state.bus.subscribe(&channel).await;
    debug!(tenant = %query.tenant, "transcript tail subscriber connected");
    ws.on_upgrade(move |socket| handle_socket(socket, rx))
}

pub async fn ws_intel(
    State(state): State<AppState>,
    Query(query): Query<WsQuery>,
    ws: WebSocketUpgrade,
) -> Response {
    if let Err(resp) = authorize(&state, &query).await {
        return resp;
    }
    let channel = bus::intel_channel(&query.tenant);
    let rx = state.bus.subscribe(&channel).await;
    debug!(tenant = %query.tenant, "intel subscriber connected");
    ws.on_upgrade(move |socket| handle_socket(socket, rx))
}

async fn handle_socket(mut socket: WebSocket, mut rx: broadcast::Receiver<Arc<str>>) {
    metrics::inc(&metrics::WS_CONNECTIONS_ACTIVE);
    loop {
        tokio::select! {
            event = rx.recv() => {
                match event {
                    Ok(payload) => {
                        if socket.send(Message::Text(payload.to_string())).await.is_err() {
                            break;
                        }
                    }
                    Err(broadcast::error::RecvError::Lagged(n)) => {
                        // Drop-slow policy: report the drop, keep the connection.
                        metrics::add(&metrics::EVENTS_DROPPED_SLOW_CONSUMER, n);
                        let notice = serde_json::json!({
                            "type": "lagged",
                            "dropped": n,
                        })
                        .to_string();
                        if socket.send(Message::Text(notice)).await.is_err() {
                            break;
                        }
                    }
                    Err(broadcast::error::RecvError::Closed) => break,
                }
            }
            incoming = socket.recv() => {
                match incoming {
                    Some(Ok(Message::Close(_))) | None => break,
                    Some(Ok(Message::Ping(p))) => {
                        if socket.send(Message::Pong(p)).await.is_err() {
                            break;
                        }
                    }
                    Some(Ok(_)) => {}
                    Some(Err(_)) => break,
                }
            }
        }
    }
    metrics::WS_CONNECTIONS_ACTIVE.fetch_sub(1, std::sync::atomic::Ordering::Relaxed);
}
