//! Fluvio WASM smart module `pii-redact` (SPEC §5).
//!
//! filter-map over transcript records on `opendesk.transcripts-raw`:
//! redacts phone numbers (E.164-ish patterns) and email addresses from the
//! `text` field, sets `redacted: true`, and passes the record through.
//! Malformed (non-JSON or schema-mismatching) records are dropped (mapped to
//! `None`) so sinks only ever see clean, redacted transcripts.

use std::sync::OnceLock;

use fluvio_smartmodule::{smartmodule, Record, RecordData, Result};
use regex::Regex;
use serde::{Deserialize, Serialize};

/// Transcript record (SPEC §4: ConversationTurn on the raw topic).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TranscriptRecord {
    #[serde(rename = "conversationId")]
    pub conversation_id: String,
    #[serde(rename = "tenantId")]
    pub tenant_id: String,
    pub role: String,
    pub text: String,
    pub ts: String,
    #[serde(rename = "audioUrl", default, skip_serializing_if = "Option::is_none")]
    pub audio_url: Option<String>,
    #[serde(default)]
    pub redacted: bool,
}

pub const PHONE_REPLACEMENT: &str = "[PHONE REDACTED]";
pub const EMAIL_REPLACEMENT: &str = "[EMAIL REDACTED]";

/// E.164-ish: optional leading `+`, then 8-15 digits allowing common
/// separators (space, dash, dot, parentheses) between digit groups.
/// Requires at least 8 digits to avoid redacting ordinary short numbers.
fn phone_regex() -> &'static Regex {
    static RE: OnceLock<Regex> = OnceLock::new();
    RE.get_or_init(|| {
        Regex::new(r"\+?\d[\d .()\-]{6,17}\d")
            .expect("phone regex compiles")
    })
}

fn email_regex() -> &'static Regex {
    static RE: OnceLock<Regex> = OnceLock::new();
    RE.get_or_init(|| {
        Regex::new(r"[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}")
            .expect("email regex compiles")
    })
}

/// Redact phone numbers and emails from free text.
pub fn redact_text(text: &str) -> String {
    let without_emails = email_regex().replace_all(text, EMAIL_REPLACEMENT);
    phone_regex()
        .replace_all(&without_emails, PHONE_REPLACEMENT)
        .into_owned()
}

/// Redact a transcript record and mark it as redacted.
pub fn redact_record(record: &TranscriptRecord) -> TranscriptRecord {
    let mut out = record.clone();
    out.text = redact_text(&record.text);
    out.redacted = true;
    out
}

#[smartmodule(filter_map)]
pub fn filter_map(record: &Record) -> Result<Option<(Option<RecordData>, RecordData)>> {
    let input: TranscriptRecord = match serde_json::from_slice(record.value.as_ref()) {
        Ok(v) => v,
        Err(_) => {
            // Drop non-conforming records rather than leaking unredacted text.
            return Ok(None);
        }
    };
    let output = redact_record(&input);
    let serialized = serde_json::to_vec(&output)?;
    Ok(Some((record.key.clone(), RecordData::from(serialized))))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample() -> TranscriptRecord {
        TranscriptRecord {
            conversation_id: "conv-1".to_string(),
            tenant_id: "acme".to_string(),
            role: "user".to_string(),
            text: "Hi, this is Jane. Call me at +1 (415) 555-0132 or email jane.doe@example.com to confirm.".to_string(),
            ts: "2024-05-01T12:34:56Z".to_string(),
            audio_url: None,
            redacted: false,
        }
    }

    #[test]
    fn redacts_phone_numbers() {
        for (input, expect_redacted) in [
            ("call +14155550132 now", true),
            ("call +1 (415) 555-0132 now", true),
            ("call +44 20 7946 0958 now", true),
            ("my number is 415-555-0132 thanks", true),
            ("order 12345 shipped", false),   // too short: not a phone
            ("we met in 2024 at 3pm", false), // not phone-shaped
        ] {
            let out = redact_text(input);
            assert_eq!(
                out.contains(PHONE_REPLACEMENT),
                expect_redacted,
                "input: {input} -> {out}"
            );
            if expect_redacted {
                assert!(!out.contains("415"), "digits must be gone: {out}");
            }
        }
    }

    #[test]
    fn redacts_emails() {
        let out = redact_text("reach me at jane.doe+ops@example.co.uk please");
        assert!(out.contains(EMAIL_REPLACEMENT));
        assert!(!out.contains("jane.doe"));
    }

    #[test]
    fn record_redaction_sets_flag_and_preserves_fields() {
        let rec = sample();
        let out = redact_record(&rec);
        assert!(out.redacted);
        assert_eq!(out.conversation_id, "conv-1");
        assert_eq!(out.tenant_id, "acme");
        assert_eq!(out.role, "user");
        assert_eq!(out.ts, rec.ts);
        assert!(out.text.contains(PHONE_REPLACEMENT));
        assert!(out.text.contains(EMAIL_REPLACEMENT));
        assert!(!out.text.contains("jane.doe@example.com"));
    }

    #[test]
    fn record_json_roundtrip_through_schema() {
        // Same shape as records on opendesk.transcripts-raw.
        let raw = br#"{
            "conversationId": "conv-9",
            "tenantId": "globex",
            "role": "agent",
            "text": "Your confirmation code was sent to +49 30 901820 and bob@globex.io",
            "ts": "2024-05-01T12:35:10Z"
        }"#;
        let parsed: TranscriptRecord = serde_json::from_slice(raw).unwrap();
        assert!(!parsed.redacted, "redacted defaults to false on the raw topic");
        let redacted = redact_record(&parsed);
        let json = serde_json::to_value(&redacted).unwrap();
        assert_eq!(json["redacted"], serde_json::Value::Bool(true));
        let text = json["text"].as_str().unwrap();
        assert!(text.contains(PHONE_REPLACEMENT));
        assert!(text.contains(EMAIL_REPLACEMENT));
    }

    #[test]
    fn clean_text_passes_through_unchanged() {
        let out = redact_text("Table for two at seven please.");
        assert_eq!(out, "Table for two at seven please.");
    }
}
