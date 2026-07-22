//! Environment-driven configuration (see README.md env table).

#[derive(Debug, Clone)]
pub struct Config {
    /// HTTP listen port (SPEC §3: 7004).
    pub port: u16,
    /// `sim` (default, ADR-0007 fallback) or `tigerbeetle` (requires `tb-live` feature).
    pub ledger_impl: String,
    pub tb_addresses: String,
    pub tb_cluster_id: u128,
    pub kafka_brokers: String,
    pub kafka_group_id: String,
    pub kafka_commands_topic: String,
    pub kafka_consumer_enabled: bool,
    pub dapr_host: String,
    pub dapr_http_port: u16,
    pub dapr_pubsub: String,
    pub events_topic: String,
    pub mojaloop_endpoint: String,
    /// Platform fee in basis points applied on captures/no-show fees.
    pub platform_fee_bps: u64,
}

fn env_or(key: &str, default: &str) -> String {
    std::env::var(key).unwrap_or_else(|_| default.to_string())
}

fn env_parse<T: std::str::FromStr>(key: &str, default: T) -> T {
    std::env::var(key)
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(default)
}

impl Config {
    pub fn from_env() -> Self {
        Self {
            port: env_parse("PORT", 7004),
            ledger_impl: env_or("LEDGER_IMPL", "sim"),
            tb_addresses: env_or("TB_ADDRESSES", "tigerbeetle:3000"),
            tb_cluster_id: env_parse("TB_CLUSTER_ID", 0),
            kafka_brokers: env_or("KAFKA_BROKERS", "kafka:9092"),
            kafka_group_id: env_or("KAFKA_GROUP_ID", "payments-service"),
            kafka_commands_topic: env_or("PAYMENTS_COMMANDS_TOPIC", "opendesk.payments.commands"),
            kafka_consumer_enabled: env_parse("KAFKA_CONSUMER_ENABLED", true),
            dapr_host: env_or("DAPR_HOST", "daprd-payments"),
            dapr_http_port: env_parse("DAPR_HTTP_PORT", 3500),
            dapr_pubsub: env_or("DAPR_PUBSUB_NAME", "pubsub-kafka"),
            events_topic: env_or("PAYMENTS_EVENTS_TOPIC", "opendesk.payments.events"),
            mojaloop_endpoint: env_or("MOJALOOP_ENDPOINT", "http://mojaloop:8444"),
            platform_fee_bps: env_parse("PLATFORM_FEE_BPS", 250),
        }
    }

    pub fn dapr_base_url(&self) -> String {
        format!("http://{}:{}", self.dapr_host, self.dapr_http_port)
    }
}
