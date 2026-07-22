//! Live TigerBeetle client (`LEDGER_IMPL=tigerbeetle`), compiled only with the
//! `tb-live` cargo feature (ADR-0007).
//!
//! Written against a documented minimal subset of the `tigerbeetle-unofficial`
//! crate (the community-published official Rust client for TigerBeetle):
//!   - `tigerbeetle_unofficial::Client::new(cluster_id: u128, addresses: &str)`
//!   - `Client::create_accounts(Vec<Account>)`, `Client::create_transfers(Vec<Transfer>)`,
//!     `Client::lookup_accounts(Vec<u128>)`
//!   - `Account::new(id: u128, ledger: u32, code: u16)` with builder
//!     `.with_flags(AccountFlags { .. })`
//!   - `Transfer::new(id, debit_account_id, credit_account_id, amount)` with
//!     builders `.with_ledger(..)`, `.with_code(..)`, `.with_pending_id(..)`,
//!     `.with_flags(TransferFlags::PENDING / POST_PENDING_TRANSFER / VOID_PENDING_TRANSFER)`
//! If the pinned crate version drifts from this surface, this module is the
//! single integration point to adjust. The default build does NOT compile it,
//! so the service always builds green (ADR-0007 fallback to the sim ledger).

use async_trait::async_trait;
use tigerbeetle_unofficial as tb;
use uuid::Uuid;

use super::*;

pub struct TigerBeetleClient {
    client: tb::Client,
    ledger_id: u32,
    fee_bps: u64,
}

fn map_err<E: std::fmt::Display>(e: E) -> LedgerError {
    LedgerError::Backend(e.to_string())
}

impl TigerBeetleClient {
    pub async fn connect(
        addresses: &str,
        cluster_id: u128,
        ledger_id: u32,
        fee_bps: u64,
    ) -> Result<Self, LedgerError> {
        let client = tb::Client::new(cluster_id, addresses).map_err(map_err)?;
        Ok(Self {
            client,
            ledger_id,
            fee_bps,
        })
    }

    fn tb_transfer(
        &self,
        id: u128,
        debit: &str,
        credit: &str,
        amount: u64,
        code: u16,
    ) -> tb::Transfer {
        tb::Transfer::new(id, account_id(debit), account_id(credit), amount)
            .with_ledger(self.ledger_id)
            .with_code(code)
    }

    async fn submit(&self, transfers: Vec<tb::Transfer>) -> Result<(), LedgerError> {
        let results = self
            .client
            .create_transfers(transfers)
            .await
            .map_err(map_err)?;
        if let Some(first) = results.first() {
            return Err(LedgerError::Backend(format!(
                "tigerbeetle transfer rejected: {first:?}"
            )));
        }
        Ok(())
    }

    fn wrap(
        &self,
        id: u128,
        debit: &str,
        credit: &str,
        amount: u64,
        code: u16,
        state: TransferState,
        flag: TransferFlag,
        pending_id: Option<u128>,
    ) -> Transfer {
        Transfer {
            id,
            debit_account: debit.to_string(),
            credit_account: credit.to_string(),
            amount,
            ledger: self.ledger_id,
            code,
            state,
            flag,
            pending_id,
            created_at: chrono::Utc::now(),
        }
    }
}

#[async_trait]
impl LedgerClient for TigerBeetleClient {
    async fn create_accounts(&self, tenant_id: &str) -> Result<Vec<Account>, LedgerError> {
        let defs = [
            (deposits_account(tenant_id), ACCOUNT_CODE_TENANT_DEPOSITS),
            (revenue_account(tenant_id), ACCOUNT_CODE_TENANT_REVENUE),
            (PLATFORM_FEES_ACCOUNT.to_string(), ACCOUNT_CODE_PLATFORM_FEES),
            (
                PLATFORM_CLEARING_ACCOUNT.to_string(),
                ACCOUNT_CODE_PLATFORM_CLEARING,
            ),
            (
                PLATFORM_PAYOUTS_ACCOUNT.to_string(),
                ACCOUNT_CODE_PLATFORM_PAYOUTS,
            ),
        ];
        let tb_accounts: Vec<tb::Account> = defs
            .iter()
            .map(|(name, code)| tb::Account::new(account_id(name), self.ledger_id, *code))
            .collect();
        let results = self
            .client
            .create_accounts(tb_accounts)
            .await
            .map_err(map_err)?;
        // `exists` results are expected on idempotent re-creation; anything
        // else is a real error.
        for r in &results {
            let msg = format!("{r:?}");
            if !msg.contains("exists") {
                return Err(LedgerError::Backend(format!(
                    "tigerbeetle account creation failed: {msg}"
                )));
            }
        }
        Ok(defs
            .iter()
            .map(|(name, code)| Account {
                id: account_id(name),
                name: name.clone(),
                ledger: self.ledger_id,
                code: *code,
                debits_pending: 0,
                credits_pending: 0,
                debits_posted: 0,
                credits_posted: 0,
            })
            .collect())
    }

    async fn hold_deposit(
        &self,
        tenant_id: &str,
        transfer_id: Uuid,
        amount: u64,
    ) -> Result<Transfer, LedgerError> {
        if amount == 0 {
            return Err(LedgerError::InvalidAmount);
        }
        let debit = PLATFORM_CLEARING_ACCOUNT.to_string();
        let credit = deposits_account(tenant_id);
        let t = self
            .tb_transfer(transfer_id.as_u128(), &debit, &credit, amount, CODE_DEPOSIT_HOLD)
            .with_flags(tb::TransferFlags::PENDING);
        self.submit(vec![t]).await?;
        Ok(self.wrap(
            transfer_id.as_u128(),
            &debit,
            &credit,
            amount,
            CODE_DEPOSIT_HOLD,
            TransferState::Pending,
            TransferFlag::None,
            None,
        ))
    }

    async fn capture(
        &self,
        tenant_id: &str,
        hold_id: Uuid,
        transfer_id: Uuid,
        amount: Option<u64>,
    ) -> Result<CaptureResult, LedgerError> {
        self.capture_like_tb(tenant_id, hold_id, transfer_id, amount, CODE_CAPTURE)
            .await
    }

    async fn refund(
        &self,
        tenant_id: &str,
        transfer_id: Uuid,
        hold_id: Option<Uuid>,
        amount: u64,
    ) -> Result<Transfer, LedgerError> {
        if let Some(h) = hold_id {
            // Void the pending hold; TigerBeetle ignores amount for voids and
            // fails with `pending_transfer_not_pending` if already resolved.
            let debit = PLATFORM_CLEARING_ACCOUNT.to_string();
            let credit = deposits_account(tenant_id);
            let t = self
                .tb_transfer(transfer_id.as_u128(), &debit, &credit, u64::MAX, CODE_REFUND)
                .with_pending_id(h.as_u128())
                .with_flags(tb::TransferFlags::VOID_PENDING_TRANSFER);
            match self.submit(vec![t]).await {
                Ok(()) => {
                    return Ok(self.wrap(
                        transfer_id.as_u128(),
                        &debit,
                        &credit,
                        0,
                        CODE_REFUND,
                        TransferState::Posted,
                        TransferFlag::VoidPending,
                        Some(h.as_u128()),
                    ))
                }
                Err(_) => { /* fall through to posted refund */ }
            }
        }
        if amount == 0 {
            return Err(LedgerError::InvalidAmount);
        }
        let debit = revenue_account(tenant_id);
        let credit = PLATFORM_CLEARING_ACCOUNT.to_string();
        let t = self.tb_transfer(transfer_id.as_u128(), &debit, &credit, amount, CODE_REFUND);
        self.submit(vec![t]).await?;
        Ok(self.wrap(
            transfer_id.as_u128(),
            &debit,
            &credit,
            amount,
            CODE_REFUND,
            TransferState::Posted,
            TransferFlag::None,
            None,
        ))
    }

    async fn no_show_fee(
        &self,
        tenant_id: &str,
        hold_id: Uuid,
        transfer_id: Uuid,
        amount: u64,
    ) -> Result<CaptureResult, LedgerError> {
        self.capture_like_tb(tenant_id, hold_id, transfer_id, Some(amount), CODE_NO_SHOW_FEE)
            .await
    }

    async fn payout(
        &self,
        tenant_id: &str,
        transfer_id: Uuid,
        amount: u64,
    ) -> Result<Transfer, LedgerError> {
        if amount == 0 {
            return Err(LedgerError::InvalidAmount);
        }
        let debit = revenue_account(tenant_id);
        let credit = PLATFORM_PAYOUTS_ACCOUNT.to_string();
        let t = self.tb_transfer(transfer_id.as_u128(), &debit, &credit, amount, CODE_PAYOUT);
        self.submit(vec![t]).await?;
        Ok(self.wrap(
            transfer_id.as_u128(),
            &debit,
            &credit,
            amount,
            CODE_PAYOUT,
            TransferState::Posted,
            TransferFlag::None,
            None,
        ))
    }

    async fn balance(&self, tenant_id: &str) -> Result<TenantBalance, LedgerError> {
        let names = [deposits_account(tenant_id), revenue_account(tenant_id)];
        let ids: Vec<u128> = names.iter().map(|n| account_id(n)).collect();
        let accounts = self.client.lookup_accounts(ids).await.map_err(map_err)?;
        let mut out = Vec::new();
        for (i, a) in accounts.iter().enumerate() {
            let debits_posted = a.debits_posted();
            let credits_posted = a.credits_posted();
            let debits_pending = a.debits_pending();
            let credits_pending = a.credits_pending();
            out.push(AccountBalance {
                account: names[i].clone(),
                id: format!("{:032x}", a.id()),
                debits_pending,
                credits_pending,
                debits_posted,
                credits_posted,
                posted_net: credits_posted as i128 - debits_posted as i128,
                pending_net: credits_pending as i128 - debits_pending as i128,
            });
        }
        Ok(TenantBalance {
            tenant_id: tenant_id.to_string(),
            accounts: out,
        })
    }
}

impl TigerBeetleClient {
    async fn capture_like_tb(
        &self,
        tenant_id: &str,
        hold_id: Uuid,
        transfer_id: Uuid,
        amount: Option<u64>,
        code: u16,
    ) -> Result<CaptureResult, LedgerError> {
        let deposits = deposits_account(tenant_id);
        let revenue = revenue_account(tenant_id);
        // Amount `0` on a post tells TigerBeetle to post the full pending amount.
        let post_amount = amount.unwrap_or(0);

        let post = self
            .tb_transfer(
                transfer_id.as_u128(),
                PLATFORM_CLEARING_ACCOUNT,
                &deposits,
                post_amount,
                code,
            )
            .with_pending_id(hold_id.as_u128())
            .with_flags(tb::TransferFlags::POST_PENDING_TRANSFER);
        self.submit(vec![post]).await?;

        // The exact posted amount is unknown when `amount` was None; for the
        // split we need it, so look the hold up via the transfer query would
        // be required. Keep it simple: require explicit amounts from callers
        // of the live client, or treat full-capture splits as the posted
        // amount supplied by the caller (routes always pass one).
        let posted = amount.ok_or_else(|| {
            LedgerError::Backend(
                "live client requires explicit capture amount for fee split".into(),
            )
        })?;
        let fee = posted * self.fee_bps / 10_000;
        let net = posted - fee;

        let rev_id = Uuid::new_v5(
            &Uuid::NAMESPACE_URL,
            format!("capture-revenue:{:032x}", transfer_id.as_u128()).as_bytes(),
        );
        let fee_id = Uuid::new_v5(
            &Uuid::NAMESPACE_URL,
            format!("capture-fee:{:032x}", transfer_id.as_u128()).as_bytes(),
        );
        let mut batch = vec![self.tb_transfer(rev_id.as_u128(), &deposits, &revenue, net, code)];
        if fee > 0 {
            batch.push(self.tb_transfer(
                fee_id.as_u128(),
                &deposits,
                PLATFORM_FEES_ACCOUNT,
                fee,
                code,
            ));
        }
        self.submit(batch).await?;

        Ok(CaptureResult {
            post: self.wrap(
                transfer_id.as_u128(),
                PLATFORM_CLEARING_ACCOUNT,
                &deposits,
                posted,
                code,
                TransferState::Posted,
                TransferFlag::PostPending,
                Some(hold_id.as_u128()),
            ),
            revenue: self.wrap(
                rev_id.as_u128(),
                &deposits,
                &revenue,
                net,
                code,
                TransferState::Posted,
                TransferFlag::None,
                None,
            ),
            platform_fee: if fee > 0 {
                Some(self.wrap(
                    fee_id.as_u128(),
                    &deposits,
                    PLATFORM_FEES_ACCOUNT,
                    fee,
                    code,
                    TransferState::Posted,
                    TransferFlag::None,
                    None,
                ))
            } else {
                None
            },
        })
    }
}
