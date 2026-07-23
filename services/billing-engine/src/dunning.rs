//! Dunning sweep (SPEC-W7 B3): interval task that flips `issued` invoices
//! older than INVOICE_DUE_DAYS to `past_due`.

use std::time::Duration;

use tokio::sync::watch;
use tracing::{error, info};

use crate::invoices;
use crate::AppState;

pub async fn run(state: AppState, mut shutdown: watch::Receiver<bool>) {
    let interval_s = state.config.dunning_interval_s.max(60);
    let due_days = state.config.invoice_due_days;
    let mut ticker = tokio::time::interval(Duration::from_secs(interval_s));
    // First tick fires immediately; skip it so boot doesn't stampede the DB
    // before the consumer has even caught up.
    ticker.tick().await;
    info!(interval_s, due_days, "dunning sweep started");

    loop {
        tokio::select! {
            changed = shutdown.changed() => {
                if changed.is_ok() {
                    info!("dunning sweep shutting down");
                }
                break;
            }
            _ = ticker.tick() => {
                match invoices::mark_overdue(&state.pool, due_days).await {
                    Ok(0) => {}
                    Ok(n) => info!(marked = n, "dunning: invoices marked past_due"),
                    Err(e) => error!(error = %e, "dunning sweep failed"),
                }
            }
        }
    }
}
