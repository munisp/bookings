"use client";

import * as React from "react";
import { ArrowUpRight, Landmark, PiggyBank, TrendingUp, Wallet } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { PageHeader } from "@/components/page-header";
import { ErrorNote } from "@/components/error-note";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableEmpty,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useToast } from "@/components/ui/toast";
import { formatMoney, titleCase } from "@/lib/utils";
import type { AccountBalance, Payout, PricingRecommendation } from "@/lib/types";

export function BillingClient({ orgSlug }: { orgSlug: string }) {
  const { toast } = useToast();
  const [balance, setBalance] = React.useState<AccountBalance | null>(null);
  const [error, setError] = React.useState<string | null>(null);
  const [amount, setAmount] = React.useState<string>("");
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(async () => {
    setError(null);
    try {
      const bal = await api.get<AccountBalance>(
        `/api/payments/v1/accounts/${orgSlug}/balance`,
      );
      setBalance(bal);
      setAmount((bal.revenue_cents / 100).toFixed(2));
    } catch (e) {
      setError(
        e instanceof ApiError ? e.message : "Failed to load billing data.",
      );
    }
  }, [orgSlug]);

  React.useEffect(() => {
    void load();
  }, [load]);

  const currency = balance?.currency ?? "USD";
  const amountCents = Math.round(parseFloat(amount || "0") * 100);
  const amountValid =
    Number.isFinite(amountCents) &&
    amountCents > 0 &&
    balance !== null &&
    amountCents <= balance.revenue_cents;

  const requestPayout = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!balance || !amountValid) return;
    setBusy(true);
    try {
      // payments-service: POST /v1/payouts { tenant_id, amount_cents?, currency? }
      const payout = await api.post<Payout>("/api/payments/v1/payouts", {
        tenant_id: balance.tenant_id,
        amount_cents: amountCents,
        currency,
      });
      toast({
        title: "Payout requested",
        description: `${formatMoney(payout.amount_cents ?? amountCents, payout.currency ?? currency)} · status ${payout.status ?? "requested"}. Settlement runs over the Mojaloop rails.`,
        variant: "success",
      });
      await load();
    } catch (err) {
      toast({
        title: "Payout failed",
        description: err instanceof ApiError ? err.message : undefined,
        variant: "destructive",
      });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <PageHeader
        title="Billing"
        description="Deposits, revenue and payouts from the TigerBeetle ledger."
      />
      {error ? <ErrorNote message={error} /> : null}

      <div className="grid gap-4 sm:grid-cols-3">
        <Card>
          <CardHeader className="flex-row items-center justify-between space-y-0 pb-2">
            <p className="text-sm font-medium text-muted-foreground">
              Deposit holds
            </p>
            <PiggyBank className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <p className="text-2xl font-bold">
              {balance ? formatMoney(balance.deposits_cents, currency) : "—"}
            </p>
            <p className="text-xs text-muted-foreground">
              Pending capture or release
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex-row items-center justify-between space-y-0 pb-2">
            <p className="text-sm font-medium text-muted-foreground">
              Available revenue
            </p>
            <Wallet className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <p className="text-2xl font-bold">
              {balance ? formatMoney(balance.revenue_cents, currency) : "—"}
            </p>
            <p className="text-xs text-muted-foreground">Ready to pay out</p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex-row items-center justify-between space-y-0 pb-2">
            <p className="text-sm font-medium text-muted-foreground">
              Paid out (lifetime)
            </p>
            <Landmark className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <p className="text-2xl font-bold">
              {balance ? formatMoney(balance.paid_out_cents, currency) : "—"}
            </p>
            <p className="text-xs text-muted-foreground">
              Settled via Mojaloop rails
            </p>
          </CardContent>
        </Card>
      </div>

      <Card className="mt-6 max-w-xl">
        <CardHeader>
          <CardTitle>Request payout</CardTitle>
          <CardDescription>
            Pay out available revenue to the tenant settlement account.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={requestPayout} className="flex flex-wrap items-end gap-3">
            <div className="grid min-w-48 flex-1 gap-1.5">
              <Label htmlFor="payout-amount">Amount ({currency})</Label>
              <Input
                id="payout-amount"
                type="number"
                min="0.01"
                step="0.01"
                value={amount}
                onChange={(e) => setAmount(e.target.value)}
                disabled={!balance || balance.revenue_cents <= 0}
              />
              {balance && amountCents > balance.revenue_cents ? (
                <p className="text-xs text-destructive">
                  Exceeds available revenue of{" "}
                  {formatMoney(balance.revenue_cents, currency)}.
                </p>
              ) : null}
            </div>
            <Button
              type="button"
              variant="outline"
              disabled={!balance || balance.revenue_cents <= 0}
              onClick={() =>
                balance && setAmount((balance.revenue_cents / 100).toFixed(2))
              }
            >
              Full amount
            </Button>
            <Button type="submit" disabled={busy || !amountValid}>
              <ArrowUpRight className="h-4 w-4" />
              {busy ? "Requesting…" : "Request payout"}
            </Button>
          </form>
        </CardContent>
      </Card>

      <RecommendationsCard orgSlug={orgSlug} />
    </div>
  );
}

/**
 * Revenue intelligence (innovation 9): pricing recommendations computed in
 * the lakehouse (gold.reco_pricing) and served by the analytics-pipeline.
 * Reached via the BFF special-case: /api/analytics/* → http://analytics:7009
 * (APISIX has no analytics route). Recommendations are human-review only —
 * nothing is auto-applied.
 */
function RecommendationsCard({ orgSlug }: { orgSlug: string }) {
  const [recs, setRecs] = React.useState<PricingRecommendation[] | null>(null);
  const [unavailable, setUnavailable] = React.useState<string | null>(null);

  React.useEffect(() => {
    (async () => {
      try {
        const data = await api.get<
          PricingRecommendation[] | { items: PricingRecommendation[] }
        >("/api/analytics/v1/recommendations", { tenant: orgSlug });
        setRecs(Array.isArray(data) ? data : (data.items ?? []));
      } catch (e) {
        setUnavailable(
          e instanceof ApiError
            ? e.message
            : "Analytics service unreachable.",
        );
      }
    })();
  }, [orgSlug]);

  return (
    <Card className="mt-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <TrendingUp className="h-4 w-4" /> Pricing recommendations
        </CardTitle>
        <CardDescription>
          Suggested peak-hour multipliers and deposit percentages from your
          booking and payment history.{" "}
          <span className="font-medium">
            Recommendation only — review and apply manually; nothing changes
            automatically.
          </span>
        </CardDescription>
      </CardHeader>
      <CardContent>
        {unavailable ? (
          <p className="text-sm text-muted-foreground">
            Recommendations are not available right now ({unavailable}). They
            appear once the lakehouse revenue-intelligence job has run.
          </p>
        ) : recs === null ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Offering</TableHead>
                <TableHead>Peak-hour multiplier</TableHead>
                <TableHead>Suggested deposit</TableHead>
                <TableHead>No-show risk</TableHead>
                <TableHead className="pr-2 text-right">Generated</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {recs.length === 0 ? (
                <TableEmpty colSpan={5}>
                  No recommendations yet — they are generated from your booking
                  and payment history once enough data has landed in the
                  lakehouse.
                </TableEmpty>
              ) : (
                recs.map((r) => (
                  <TableRow key={r.offering_id}>
                    <TableCell className="font-medium">
                      {r.offering_name ?? r.offering_id}
                    </TableCell>
                    <TableCell>×{r.peak_hour_multiplier.toFixed(2)}</TableCell>
                    <TableCell>{r.suggested_deposit_pct}%</TableCell>
                    <TableCell>
                      {r.no_show_risk_band ? (
                        <Badge
                          variant={
                            r.no_show_risk_band === "high"
                              ? "destructive"
                              : r.no_show_risk_band === "medium"
                                ? "warning"
                                : "secondary"
                          }
                        >
                          {titleCase(r.no_show_risk_band)}
                        </Badge>
                      ) : (
                        "—"
                      )}
                    </TableCell>
                    <TableCell className="pr-2 text-right text-xs text-muted-foreground">
                      {r.generated_at
                        ? new Date(r.generated_at).toLocaleDateString()
                        : "—"}
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
