//! Per-tenant broadcast bus with a drop-slow backpressure policy.
//!
//! Each tenant channel is a `tokio::sync::broadcast` ring buffer. Slow
//! consumers that fall more than `capacity` messages behind receive
//! `RecvError::Lagged` (their oldest messages are dropped) and the dropped
//! count is exported via `/metrics`.

use std::collections::HashMap;
use std::sync::Arc;

use tokio::sync::{broadcast, RwLock};

use crate::metrics;

pub const BOOKING_CHANNEL_PREFIX: &str = "booking:";
pub const TRANSCRIPTS_CHANNEL_PREFIX: &str = "transcripts:";
pub const INTEL_CHANNEL_PREFIX: &str = "intel:";

pub fn booking_channel(tenant: &str) -> String {
    format!("{BOOKING_CHANNEL_PREFIX}{tenant}")
}

pub fn transcripts_channel(tenant: &str) -> String {
    format!("{TRANSCRIPTS_CHANNEL_PREFIX}{tenant}")
}

pub fn intel_channel(tenant: &str) -> String {
    format!("{INTEL_CHANNEL_PREFIX}{tenant}")
}

pub struct EventBus {
    channels: RwLock<HashMap<String, broadcast::Sender<Arc<str>>>>,
    capacity: usize,
}

impl EventBus {
    pub fn new(capacity: usize) -> Self {
        Self {
            channels: RwLock::new(HashMap::new()),
            capacity,
        }
    }

    /// Subscribe to a channel, creating it on demand.
    pub async fn subscribe(&self, channel: &str) -> broadcast::Receiver<Arc<str>> {
        {
            let channels = self.channels.read().await;
            if let Some(tx) = channels.get(channel) {
                return tx.subscribe();
            }
        }
        let mut channels = self.channels.write().await;
        let tx = channels
            .entry(channel.to_string())
            .or_insert_with(|| broadcast::channel(self.capacity).0);
        tx.subscribe()
    }

    /// Publish an event to a channel. Returns the number of active receivers
    /// that accepted it. Publishing to a channel with no subscribers is a
    /// no-op counted as `no_subscriber`.
    pub async fn publish(&self, channel: &str, payload: String) -> usize {
        let tx = {
            let channels = self.channels.read().await;
            channels.get(channel).cloned()
        };
        match tx {
            Some(tx) => match tx.send(Arc::from(payload.as_str())) {
                Ok(receivers) => {
                    metrics::inc(&metrics::EVENTS_PUBLISHED);
                    receivers
                }
                Err(_) => {
                    metrics::inc(&metrics::EVENTS_NO_SUBSCRIBER);
                    0
                }
            },
            None => {
                metrics::inc(&metrics::EVENTS_NO_SUBSCRIBER);
                0
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn publish_reaches_subscribers_per_tenant() {
        let bus = EventBus::new(16);
        let mut rx_a = bus.subscribe(&booking_channel("acme")).await;
        let mut rx_b = bus.subscribe(&booking_channel("globex")).await;
        bus.publish(&booking_channel("acme"), "{\"n\":1}".to_string()).await;
        let got = rx_a.recv().await.unwrap();
        assert_eq!(&*got, "{\"n\":1}");
        // globex subscriber has nothing.
        assert!(rx_b.try_recv().is_err());
    }

    #[tokio::test]
    async fn slow_consumers_are_dropped_with_lagged() {
        let bus = EventBus::new(2);
        let mut rx = bus.subscribe(&booking_channel("acme")).await;
        for i in 0..4 {
            bus.publish(&booking_channel("acme"), format!("m{i}")).await;
        }
        // Capacity 2, never received => oldest dropped.
        match rx.recv().await {
            Err(broadcast::error::RecvError::Lagged(n)) => assert!(n >= 1),
            other => panic!("expected Lagged, got {other:?}"),
        }
    }

    #[tokio::test]
    async fn publish_without_subscribers_is_noop() {
        let bus = EventBus::new(4);
        let receivers = bus.publish(&booking_channel("nobody"), "x".to_string()).await;
        assert_eq!(receivers, 0);
    }
}
