//! CloudEvents 1.0 envelope (SPEC §4).

use chrono::Utc;
use serde::Serialize;
use uuid::Uuid;

#[derive(Debug, Clone, Serialize)]
pub struct CloudEvent<T: Serialize> {
    pub specversion: &'static str,
    pub id: String,
    pub source: String,
    #[serde(rename = "type")]
    pub type_: String,
    pub subject: String,
    pub time: String,
    /// CloudEvents extension attribute carrying the tenant (SPEC §4).
    pub tenantid: String,
    pub data: T,
}

impl<T: Serialize> CloudEvent<T> {
    pub fn new(source: &str, type_: &str, subject: &str, tenant_id: &str, data: T) -> Self {
        Self {
            specversion: "1.0",
            id: Uuid::new_v4().to_string(),
            source: source.to_string(),
            type_: type_.to_string(),
            subject: subject.to_string(),
            time: Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Millis, true),
            tenantid: tenant_id.to_string(),
            data,
        }
    }
}
