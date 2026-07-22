//! Primary event source: Kafka `opendesk.booking.events` (CloudEvents JSON,
//! SPEC §4). Events are fanned out to the tenant's `booking:{slug}` channel.

use std::sync::Arc;

use rdkafka::consumer::{CommitMode, Consumer, StreamConsumer};
use rdkafka::Message as _;
use serde::Deserialize;
use tokio::sync::watch;
use tracing::{debug, error, info, warn};

use crate::bus;
use crate::bus::EventBus;
use crate::metrics;

#[derive(Debug, Deserialize)]
pub(crate) struct RawCloudEvent {
    #[serde(default)]
    tenantid: Option<String>,
    #[serde(default)]
    data: serde_json::Value,
}

/// Tenant slug for routing: CloudEvents `tenantid` extension first, then
/// common `data.tenantId` / `data.tenant_id` fallbacks. Shared with the
/// enriched-turns consumer (enriched_consumer.rs).
pub(crate) fn extract_tenant(event: &RawCloudEvent) -> Option<String> {
    if let Some(t) = &event.tenantid {
        if !t.is_empty() {
            return Some(t.clone());
        }
    }
    event
        .data
        .get("tenantId")
        .or_else(|| event.data.get("tenant_id"))
        .and_then(|v| v.as_str())
        .map(|s| s.to_string())
}

pub async fn run(
    bus: Arc<EventBus>,
    brokers: String,
    group_id: String,
    topic: String,
    mut shutdown: watch::Receiver<bool>,
) {
    let consumer: StreamConsumer = match rdkafka::config::ClientConfig::new()
        .set("group.id", &group_id)
        .set("bootstrap.servers", &brokers)
        .set("enable.auto.commit", "true")
        .set("auto.offset.reset", "latest")
        .set("session.timeout.ms", "10000")
        .create()
    {
        Ok(c) => c,
        Err(e) => {
            error!(error = %e, "failed to create kafka consumer; booking fan-out disabled");
            return;
        }
    };
    if let Err(e) = consumer.subscribe(&[&topic]) {
        error!(error = %e, topic = %topic, "failed to subscribe");
        return;
    }
    info!(topic = %topic, brokers = %brokers, "booking events consumer started");

    loop {
        tokio::select! {
            changed = shutdown.changed() => {
                if changed.is_ok() {
                    info!("booking events consumer shutting down");
                }
                break;
            }
            msg = consumer.recv() => {
                match msg {
                    Ok(m) => {
                        metrics::inc(&metrics::KAFKA_MESSAGES_TOTAL);
                        let payload = m.payload().unwrap_or_default();
                        match serde_json::from_slice::<RawCloudEvent>(payload) {
                            Ok(event) => {
                                match extract_tenant(&event) {
                                    Some(tenant) => {
                                        let raw = String::from_utf8_lossy(payload).into_owned();
                                        let n = bus.publish(&bus::booking_channel(&tenant), raw).await;
                                        debug!(tenant = %tenant, receivers = n, "fanned out booking event");
                                    }
                                    None => {
                                        debug!("booking event without tenant id; dropped");
                                    }
                                }
                            }
                            Err(e) => {
                                warn!(error = %e, "unparseable booking event; skipped");
                            }
                        }
                        let _ = consumer.commit_message(&m, CommitMode::Async);
                    }
                    Err(e) => {
                        warn!(error = %e, "kafka receive error");
                        tokio::time::sleep(std::time::Duration::from_millis(500)).await;
                    }
                }
            }
        }
    }
}
