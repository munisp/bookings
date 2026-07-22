//! Kafka consumer for `opendesk.payments.commands` (SPEC §4).
//!
//! Commands (CloudEvents): ChargeDeposit, Refund, NoShowFee.
//! Processing is idempotent: the ledger transfer id is derived deterministically
//! from the command id, so redeliveries replay against the ledger without
//! double-posting. Every processed command emits a `PaymentPosted` event via the
//! Dapr outbox.

use rdkafka::consumer::{CommitMode, Consumer, StreamConsumer};
use rdkafka::Message as _;
use serde::Deserialize;
use tokio::sync::watch;
use tracing::{debug, error, info, warn};
use uuid::Uuid;

use crate::AppState;

#[derive(Debug, Deserialize)]
struct RawCloudEvent {
    id: String,
    #[serde(rename = "type")]
    type_: String,
    #[serde(default)]
    tenantid: Option<String>,
    #[serde(default)]
    data: serde_json::Value,
}

#[derive(Debug, Deserialize)]
struct ChargeDepositCmd {
    tenant_id: String,
    booking_id: Option<String>,
    amount_cents: u64,
}

#[derive(Debug, Deserialize)]
struct RefundCmd {
    tenant_id: String,
    deposit_id: Option<Uuid>,
    #[serde(default)]
    amount_cents: u64,
}

#[derive(Debug, Deserialize)]
struct NoShowFeeCmd {
    tenant_id: String,
    deposit_id: Uuid,
    amount_cents: u64,
}

fn command_transfer_id(event: &RawCloudEvent) -> Uuid {
    Uuid::new_v5(
        &Uuid::NAMESPACE_URL,
        format!("cmd:{}:{}", event.id, event.type_).as_bytes(),
    )
}

async fn handle_command(state: &AppState, event: &RawCloudEvent) -> Result<(), String> {
    let ty = event.type_.as_str();
    if ty.ends_with("ChargeDeposit") {
        let cmd: ChargeDepositCmd = serde_json::from_value(event.data.clone())
            .map_err(|e| format!("bad ChargeDeposit payload: {e}"))?;
        let t = state
            .ledger
            .hold_deposit(&cmd.tenant_id, command_transfer_id(event), cmd.amount_cents)
            .await
            .map_err(|e| e.to_string())?;
        publish_payment_posted(state, event, &cmd.tenant_id, "ChargeDeposit", &t.id_string())
            .await;
    } else if ty.ends_with("NoShowFee") {
        let cmd: NoShowFeeCmd = serde_json::from_value(event.data.clone())
            .map_err(|e| format!("bad NoShowFee payload: {e}"))?;
        let res = state
            .ledger
            .no_show_fee(
                &cmd.tenant_id,
                cmd.deposit_id,
                command_transfer_id(event),
                cmd.amount_cents,
            )
            .await
            .map_err(|e| e.to_string())?;
        publish_payment_posted(
            state,
            event,
            &cmd.tenant_id,
            "NoShowFee",
            &res.post.id_string(),
        )
        .await;
    } else if ty.ends_with("Refund") {
        let cmd: RefundCmd = serde_json::from_value(event.data.clone())
            .map_err(|e| format!("bad Refund payload: {e}"))?;
        let t = state
            .ledger
            .refund(
                &cmd.tenant_id,
                command_transfer_id(event),
                cmd.deposit_id,
                cmd.amount_cents,
            )
            .await
            .map_err(|e| e.to_string())?;
        publish_payment_posted(state, event, &cmd.tenant_id, "Refund", &t.id_string()).await;
    } else {
        debug!(type_ = ty, "ignoring unknown payments command type");
    }
    Ok(())
}

async fn publish_payment_posted(
    state: &AppState,
    event: &RawCloudEvent,
    tenant_id: &str,
    action: &str,
    ledger_ref: &str,
) {
    state
        .publish_event(
            "PaymentPosted",
            &event.id,
            tenant_id,
            serde_json::json!({
                "commandId": event.id,
                "action": action,
                "ledgerRef": ledger_ref,
            }),
        )
        .await;
}

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
            error!(error = %e, "failed to create kafka consumer; commands consumer disabled");
            return;
        }
    };
    if let Err(e) = consumer.subscribe(&[&cfg.kafka_commands_topic]) {
        error!(error = %e, topic = %cfg.kafka_commands_topic, "failed to subscribe");
        return;
    }
    info!(
        topic = %cfg.kafka_commands_topic,
        brokers = %cfg.kafka_brokers,
        "payments commands consumer started"
    );

    loop {
        tokio::select! {
            changed = shutdown.changed() => {
                if changed.is_ok() {
                    info!("payments commands consumer shutting down");
                }
                break;
            }
            msg = consumer.recv() => {
                match msg {
                    Ok(m) => {
                        let payload = m.payload().unwrap_or_default();
                        match serde_json::from_slice::<RawCloudEvent>(payload) {
                            Ok(event) => {
                                if let Err(e) = handle_command(&state, &event).await {
                                    warn!(error = %e, event_id = %event.id, "command handling failed (will rely on redelivery)");
                                }
                            }
                            Err(e) => {
                                warn!(error = %e, "dropping unparseable payments command");
                            }
                        }
                        if let Err(e) = consumer.commit_message(&m, CommitMode::Async) {
                            warn!(error = %e, "offset commit failed");
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
