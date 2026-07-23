//! Kafka consumer for `opendesk.usage.events` (SPEC-W7 B1), mirroring
//! payments-service's consumer loop (same rdkafka 0.36 API usage).
//!
//! Offset discipline: offsets are committed only after the event is durably
//! recorded (or positively identified as a duplicate/poison). Transient DB
//! failures leave the offset uncommitted so the event is redelivered.

use rdkafka::consumer::{CommitMode, Consumer, StreamConsumer};
use rdkafka::Message as _;
use tokio::sync::watch;
use tracing::{debug, error, info, warn};

use crate::metering::{self, UsageOutcome};
use crate::models::RawCloudEvent;
use crate::AppState;

pub async fn run(state: AppState, mut shutdown: watch::Receiver<bool>) {
    let cfg = state.config.clone();
    let consumer: StreamConsumer = match rdkafka::config::ClientConfig::new()
        .set("group.id", &cfg.kafka_group_id)
        .set("bootstrap.servers", &cfg.kafka_brokers)
        .set("enable.auto.commit", "false")
        .set("auto.offset.reset", "earliest")
        .set("session.timeout.ms", "10000")
        .create()
    {
        Ok(c) => c,
        Err(e) => {
            error!(error = %e, "failed to create kafka consumer; metering consumer disabled");
            return;
        }
    };
    if let Err(e) = consumer.subscribe(&[&cfg.usage_events_topic]) {
        error!(error = %e, topic = %cfg.usage_events_topic, "failed to subscribe");
        return;
    }
    info!(
        topic = %cfg.usage_events_topic,
        brokers = %cfg.kafka_brokers,
        group = %cfg.kafka_group_id,
        "billing usage consumer started"
    );

    loop {
        tokio::select! {
            changed = shutdown.changed() => {
                if changed.is_ok() {
                    info!("billing usage consumer shutting down");
                }
                break;
            }
            msg = consumer.recv() => {
                match msg {
                    Ok(m) => {
                        let payload = m.payload().unwrap_or_default();
                        // `commit` gates the offset commit: transient failures
                        // are left for redelivery, duplicates/poison commit.
                        let commit = match serde_json::from_slice::<RawCloudEvent>(payload) {
                            Ok(event) => {
                                match metering::record_usage(&state.pool, &event).await {
                                    Ok(UsageOutcome::Recorded) => {
                                        debug!(event_id = %event.id, "usage recorded");
                                        true
                                    }
                                    Ok(UsageOutcome::Duplicate) => {
                                        debug!(event_id = %event.id, "duplicate usage event skipped");
                                        true
                                    }
                                    Err(e) => {
                                        warn!(error = %e, event_id = %event.id,
                                            "usage record failed (will rely on redelivery)");
                                        false
                                    }
                                }
                            }
                            Err(e) => {
                                warn!(error = %e, "dropping unparseable usage event");
                                true
                            }
                        };
                        if commit {
                            if let Err(e) = consumer.commit_message(&m, CommitMode::Async) {
                                warn!(error = %e, "offset commit failed");
                            }
                        }
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
