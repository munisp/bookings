//! Minimal Prometheus text exposition (no external metrics crate; SPEC §15
//! allows a simple text handler).

use std::sync::atomic::{AtomicU64, Ordering};

pub static WS_CONNECTIONS_ACTIVE: AtomicU64 = AtomicU64::new(0);
pub static EVENTS_PUBLISHED: AtomicU64 = AtomicU64::new(0);
pub static EVENTS_DROPPED_SLOW_CONSUMER: AtomicU64 = AtomicU64::new(0);
pub static EVENTS_NO_SUBSCRIBER: AtomicU64 = AtomicU64::new(0);
pub static AUTH_FAILURES: AtomicU64 = AtomicU64::new(0);
pub static KAFKA_MESSAGES_TOTAL: AtomicU64 = AtomicU64::new(0);
pub static FLUVIO_RECORDS_TOTAL: AtomicU64 = AtomicU64::new(0);

pub fn inc(counter: &'static AtomicU64) {
    counter.fetch_add(1, Ordering::Relaxed);
}

pub fn add(counter: &'static AtomicU64, n: u64) {
    counter.fetch_add(n, Ordering::Relaxed);
}

pub fn render() -> String {
    let g = |c: &'static AtomicU64| c.load(Ordering::Relaxed);
    let mut out = String::new();
    let gauge = |out: &mut String, name: &str, help: &str, ty: &str, value: u64| {
        out.push_str(&format!("# HELP {name} {help}\n# TYPE {name} {ty}\n{name} {value}\n"));
    };
    gauge(
        &mut out,
        "gateway_ws_connections_active",
        "Active WebSocket connections",
        "gauge",
        g(&WS_CONNECTIONS_ACTIVE),
    );
    gauge(
        &mut out,
        "gateway_events_published_total",
        "Events published to tenant channels",
        "counter",
        g(&EVENTS_PUBLISHED),
    );
    gauge(
        &mut out,
        "gateway_events_dropped_slow_consumer_total",
        "Events dropped for slow (lagging) WebSocket consumers",
        "counter",
        g(&EVENTS_DROPPED_SLOW_CONSUMER),
    );
    gauge(
        &mut out,
        "gateway_events_no_subscriber_total",
        "Events with no active tenant subscriber",
        "counter",
        g(&EVENTS_NO_SUBSCRIBER),
    );
    gauge(
        &mut out,
        "gateway_auth_failures_total",
        "WebSocket authentication/authorization failures",
        "counter",
        g(&AUTH_FAILURES),
    );
    gauge(
        &mut out,
        "gateway_kafka_messages_total",
        "Kafka messages consumed",
        "counter",
        g(&KAFKA_MESSAGES_TOTAL),
    );
    gauge(
        &mut out,
        "gateway_fluvio_records_total",
        "Fluvio records consumed",
        "counter",
        g(&FLUVIO_RECORDS_TOTAL),
    );
    out
}
