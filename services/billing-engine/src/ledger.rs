//! Billing ledger (SPEC-W7 B2): receivables double-entry behind the same
//! SimLedgerClient trait pattern as payments-service (ADR-0007).
//!
//! Accounts (ledger codes per SPEC-W7):
//!   200 = AR-control      `tenant:{id}:ar`        (asset: money owed to us)
//!   201 = revenue         `tenant:{id}:revenue`   (income)
//!   202 = payments-clearing `platform:billing:clearing` (cash received)
//!
//! Postings:
//!   invoice issued -> DR AR-control   / CR revenue           (code 200)
//!   invoice paid   -> DR clearing     / CR AR-control        (code 202)
//!
//! Transfers are posted (single-phase) and idempotent by transfer id, which
//! callers derive deterministically from the invoice id — webhook retries and
//! consumer redeliveries replay without double-posting.

use std::collections::HashMap;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use serde::Serialize;
use thiserror::Error;
use tokio::sync::Mutex;
use uuid::Uuid;

pub const LEDGER_ID: u32 = 1;

// Account codes (SPEC-W7 B2).
pub const ACCOUNT_CODE_AR_CONTROL: u16 = 200;
pub const ACCOUNT_CODE_REVENUE: u16 = 201;
pub const ACCOUNT_CODE_CLEARING: u16 = 202;

// Transfer codes: the debited control account's code.
pub const CODE_INVOICE_ISSUED: u16 = 200; // DR AR / CR revenue
pub const CODE_INVOICE_PAID: u16 = 202; // DR clearing / CR AR

pub fn ar_account(tenant_id: &str) -> String {
    format!("tenant:{tenant_id}:ar")
}
pub fn revenue_account(tenant_id: &str) -> String {
    format!("tenant:{tenant_id}:revenue")
}
pub const CLEARING_ACCOUNT: &str = "platform:billing:clearing";

#[derive(Debug, Clone, Serialize)]
pub struct Account {
    pub id: u128,
    pub name: String,
    pub ledger: u32,
    pub code: u16,
    pub debits_posted: u64,
    pub credits_posted: u64,
}

#[derive(Debug, Clone, Serialize)]
pub struct Transfer {
    pub id: u128,
    pub debit_account: String,
    pub credit_account: String,
    /// Amount in minor units (cents).
    pub amount: u64,
    pub ledger: u32,
    pub code: u16,
    pub created_at: DateTime<Utc>,
}

impl Transfer {
    pub fn id_string(&self) -> String {
        format!("{:032x}", self.id)
    }
}

#[derive(Debug, Clone, Serialize)]
pub struct AccountBalance {
    pub account: String,
    pub debits_posted: u64,
    pub credits_posted: u64,
    /// credits_posted - debits_posted.
    pub posted_net: i128,
}

#[derive(Debug, Clone, Serialize)]
pub struct TenantBalance {
    pub tenant_id: String,
    pub accounts: Vec<AccountBalance>,
}

#[derive(Debug, Error)]
pub enum LedgerError {
    #[error("transfer id {0} already exists with different parameters")]
    ExistsWithDifferentParameters(String),
    #[error("amount must be greater than zero")]
    InvalidAmount,
    #[error("ledger backend error: {0}")]
    Backend(String),
}

/// The receivables posting interface. `post` is the primitive; the two
/// helpers encode the SPEC-W7 posting rules.
#[async_trait]
pub trait BillingLedger: Send + Sync {
    async fn post(
        &self,
        transfer_id: Uuid,
        debit: &str,
        credit: &str,
        amount: u64,
        code: u16,
    ) -> Result<Transfer, LedgerError>;

    async fn balance(&self, tenant_id: &str) -> Result<TenantBalance, LedgerError>;

    /// Invoice issued: DR AR-control / CR revenue (code 200).
    async fn invoice_issued(
        &self,
        tenant_id: &str,
        invoice_id: Uuid,
        amount: u64,
    ) -> Result<Transfer, LedgerError> {
        let id = Uuid::new_v5(
            &Uuid::NAMESPACE_URL,
            format!("billing-issued:{invoice_id}").as_bytes(),
        );
        self.post(
            id,
            &ar_account(tenant_id),
            &revenue_account(tenant_id),
            amount,
            CODE_INVOICE_ISSUED,
        )
        .await
    }

    /// Invoice paid: DR payments-clearing / CR AR-control (code 202).
    async fn invoice_paid(
        &self,
        tenant_id: &str,
        invoice_id: Uuid,
        amount: u64,
    ) -> Result<Transfer, LedgerError> {
        let id = Uuid::new_v5(
            &Uuid::NAMESPACE_URL,
            format!("billing-paid:{invoice_id}").as_bytes(),
        );
        self.post(
            id,
            CLEARING_ACCOUNT,
            &ar_account(tenant_id),
            amount,
            CODE_INVOICE_PAID,
        )
        .await
    }
}

/// Deterministic 128-bit account id from the account name (same derivation as
/// payments-service, so a future TigerBeetle backend can share ids).
fn account_id(name: &str) -> u128 {
    Uuid::new_v5(&Uuid::NAMESPACE_URL, name.as_bytes()).as_u128()
}

// ---------------------------------------------------------------------------
// SimLedgerClient: in-memory double-entry default (ADR-0007 fallback).
// ---------------------------------------------------------------------------

#[derive(Debug, Default, Clone)]
struct SimState {
    accounts: HashMap<String, Account>,
    transfers: HashMap<u128, Transfer>,
}

pub struct SimLedgerClient {
    state: Mutex<SimState>,
    ledger_id: u32,
}

impl SimLedgerClient {
    pub fn new() -> Self {
        Self {
            state: Mutex::new(SimState::default()),
            ledger_id: LEDGER_ID,
        }
    }

    #[cfg(test)]
    async fn snapshot(&self) -> SimState {
        self.state.lock().await.clone()
    }
}

impl Default for SimLedgerClient {
    fn default() -> Self {
        Self::new()
    }
}

#[async_trait]
impl BillingLedger for SimLedgerClient {
    async fn post(
        &self,
        transfer_id: Uuid,
        debit: &str,
        credit: &str,
        amount: u64,
        code: u16,
    ) -> Result<Transfer, LedgerError> {
        if amount == 0 {
            return Err(LedgerError::InvalidAmount);
        }
        let mut st = self.state.lock().await;

        // Idempotent replay: same id + same parameters returns the recorded
        // transfer; same id + different parameters is a conflict.
        if let Some(existing) = st.transfers.get(&transfer_id.as_u128()) {
            let same = existing.debit_account == debit
                && existing.credit_account == credit
                && existing.amount == amount
                && existing.code == code;
            if same {
                return Ok(existing.clone());
            }
            return Err(LedgerError::ExistsWithDifferentParameters(
                transfer_id.to_string(),
            ));
        }

        let code_for = |name: &str| -> u16 {
            if name.ends_with(":ar") {
                ACCOUNT_CODE_AR_CONTROL
            } else if name.ends_with(":revenue") {
                ACCOUNT_CODE_REVENUE
            } else {
                ACCOUNT_CODE_CLEARING
            }
        };
        for name in [debit, credit] {
            st.accounts.entry(name.to_string()).or_insert_with(|| Account {
                id: account_id(name),
                name: name.to_string(),
                ledger: self.ledger_id,
                code: code_for(name),
                debits_posted: 0,
                credits_posted: 0,
            });
        }

        let t = Transfer {
            id: transfer_id.as_u128(),
            debit_account: debit.to_string(),
            credit_account: credit.to_string(),
            amount,
            ledger: self.ledger_id,
            code,
            created_at: Utc::now(),
        };
        st.accounts
            .get_mut(debit)
            .expect("account just ensured")
            .debits_posted += amount;
        st.accounts
            .get_mut(credit)
            .expect("account just ensured")
            .credits_posted += amount;
        st.transfers.insert(t.id, t.clone());
        Ok(t)
    }

    async fn balance(&self, tenant_id: &str) -> Result<TenantBalance, LedgerError> {
        let st = self.state.lock().await;
        let prefix = format!("tenant:{tenant_id}:");
        let mut accounts: Vec<AccountBalance> = st
            .accounts
            .values()
            .filter(|a| a.name.starts_with(&prefix))
            .map(|a| AccountBalance {
                account: a.name.clone(),
                debits_posted: a.debits_posted,
                credits_posted: a.credits_posted,
                posted_net: a.credits_posted as i128 - a.debits_posted as i128,
            })
            .collect();
        accounts.sort_by(|a, b| a.account.cmp(&b.account));
        Ok(TenantBalance {
            tenant_id: tenant_id.to_string(),
            accounts,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const TENANT: &str = "t-1";

    async fn assert_conservation(client: &SimLedgerClient) {
        let st = client.snapshot().await;
        let (mut d, mut c) = (0u128, 0u128);
        for a in st.accounts.values() {
            d += a.debits_posted as u128;
            c += a.credits_posted as u128;
        }
        assert_eq!(d, c, "double-entry conservation violated");
    }

    #[tokio::test]
    async fn issued_then_paid_moves_ar_to_clearing() {
        let c = SimLedgerClient::new();
        let invoice = Uuid::new_v4();
        let t1 = c.invoice_issued(TENANT, invoice, 12_500).await.unwrap();
        assert_eq!(t1.code, CODE_INVOICE_ISSUED);
        assert_eq!(t1.debit_account, ar_account(TENANT));
        assert_eq!(t1.credit_account, revenue_account(TENANT));
        assert_conservation(&c).await;

        let t2 = c.invoice_paid(TENANT, invoice, 12_500).await.unwrap();
        assert_eq!(t2.code, CODE_INVOICE_PAID);
        assert_eq!(t2.debit_account, CLEARING_ACCOUNT);
        assert_eq!(t2.credit_account, ar_account(TENANT));
        assert_conservation(&c).await;

        // AR nets to zero once paid; revenue carries the income.
        let bal = c.balance(TENANT).await.unwrap();
        let ar = bal
            .accounts
            .iter()
            .find(|a| a.account == ar_account(TENANT))
            .unwrap();
        assert_eq!(ar.posted_net, 0);
        let revenue = bal
            .accounts
            .iter()
            .find(|a| a.account == revenue_account(TENANT))
            .unwrap();
        assert_eq!(revenue.posted_net, 12_500);
    }

    #[tokio::test]
    async fn postings_are_idempotent_by_transfer_id() {
        let c = SimLedgerClient::new();
        let invoice = Uuid::new_v4();
        // Same invoice issued/paid twice (webhook retry): same derived
        // transfer ids replay without double-posting.
        c.invoice_issued(TENANT, invoice, 5_000).await.unwrap();
        c.invoice_issued(TENANT, invoice, 5_000).await.unwrap();
        c.invoice_paid(TENANT, invoice, 5_000).await.unwrap();
        c.invoice_paid(TENANT, invoice, 5_000).await.unwrap();
        assert_conservation(&c).await;
        let st = c.snapshot().await;
        assert_eq!(st.transfers.len(), 2, "no duplicate transfers");
        let clearing = st.accounts.get(CLEARING_ACCOUNT).unwrap();
        assert_eq!(clearing.debits_posted, 5_000);
    }

    #[tokio::test]
    async fn conflicting_replay_and_zero_amount_are_rejected() {
        let c = SimLedgerClient::new();
        let id = Uuid::new_v4();
        c.post(id, "a:x:ar", "a:x:revenue", 100, CODE_INVOICE_ISSUED)
            .await
            .unwrap();
        let err = c
            .post(id, "a:x:ar", "a:x:revenue", 200, CODE_INVOICE_ISSUED)
            .await
            .unwrap_err();
        assert!(matches!(err, LedgerError::ExistsWithDifferentParameters(_)));
        let err = c
            .post(Uuid::new_v4(), "a:x:ar", "a:x:revenue", 0, CODE_INVOICE_ISSUED)
            .await
            .unwrap_err();
        assert!(matches!(err, LedgerError::InvalidAmount));
    }
}
