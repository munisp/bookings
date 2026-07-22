"use client";

import * as React from "react";
import { CalendarX, KeyRound, LogOut, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ErrorNote } from "@/components/error-note";
import { formatDateTime } from "@/lib/utils";
import type { Booking, PublicSite } from "@/lib/types";

/**
 * Customer self-service portal (Wave 5 #7): magic-code login (SMS/email) →
 * view/reschedule/cancel own bookings. No account — the 15-minute portal
 * JWT lives in sessionStorage (per site) and rides to booking-service in
 * the X-Portal-Token header (the BFF strips Authorization).
 */

type Step = "request" | "verify" | "bookings";

interface PortalSession {
  token: string;
  contactName: string;
  expiresAt: number; // epoch ms
}

const sessionKey = (siteSlug: string) => `opendesk_portal_${siteSlug}`;

function loadSession(siteSlug: string): PortalSession | null {
  try {
    const raw = sessionStorage.getItem(sessionKey(siteSlug));
    if (!raw) return null;
    const s = JSON.parse(raw) as PortalSession;
    if (!s.token || s.expiresAt <= Date.now()) return null;
    return s;
  } catch {
    return null;
  }
}

/** fetch wrapper that attaches the portal token and parses JSON errors. */
async function portalFetch<T>(
  method: string,
  path: string,
  opts: { token?: string; body?: unknown } = {},
): Promise<T> {
  const headers: Record<string, string> = {};
  if (opts.body !== undefined) headers["content-type"] = "application/json";
  if (opts.token) headers["x-portal-token"] = opts.token;
  const res = await fetch(path, {
    method,
    headers,
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    cache: "no-store",
  });
  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  if (!res.ok) {
    const msg =
      typeof data === "object" && data !== null && "error" in data
        ? String((data as { error: unknown }).error)
        : `Request failed (${res.status})`;
    const err = new Error(msg) as Error & { status?: number };
    err.status = res.status;
    throw err;
  }
  return data as T;
}

export function PortalClient({ site }: { site: PublicSite }) {
  const [step, setStep] = React.useState<Step>("request");
  const [channel, setChannel] = React.useState<"phone" | "email">("phone");
  const [identifier, setIdentifier] = React.useState("");
  const [code, setCode] = React.useState("");
  const [session, setSession] = React.useState<PortalSession | null>(null);
  const [bookings, setBookings] = React.useState<Booking[]>([]);
  const [error, setError] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [rescheduleFor, setRescheduleFor] = React.useState<string | null>(null);
  const [newStart, setNewStart] = React.useState("");

  // Restore an existing portal session (page reload within 15 minutes).
  React.useEffect(() => {
    const s = loadSession(site.site_slug);
    if (s) {
      setSession(s);
      setStep("bookings");
    }
  }, [site.site_slug]);

  const loadBookings = React.useCallback(
    async (s: PortalSession) => {
      const data = await portalFetch<{ bookings: Booking[] }>(
        "GET",
        "/api/bookings/portal/bookings",
        { token: s.token },
      );
      setBookings(data.bookings ?? []);
    },
    [],
  );

  React.useEffect(() => {
    if (step === "bookings" && session) {
      loadBookings(session).catch((e) => {
        if ((e as { status?: number }).status === 401) {
          logout();
        } else {
          setError(e instanceof Error ? e.message : "Failed to load bookings.");
        }
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [step, session, loadBookings]);

  const logout = () => {
    sessionStorage.removeItem(sessionKey(site.site_slug));
    setSession(null);
    setBookings([]);
    setCode("");
    setStep("request");
  };

  const requestCode = async () => {
    setBusy(true);
    setError(null);
    try {
      await portalFetch(
        "POST",
        `/api/bookings/public/sites/${site.site_slug}/portal/request`,
        { body: channel === "phone" ? { phone: identifier.trim() } : { email: identifier.trim() } },
      );
      setStep("verify");
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not send the code.");
    } finally {
      setBusy(false);
    }
  };

  const verifyCode = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await portalFetch<{
        portal_token: string;
        expires_in: number;
        contact_name: string;
      }>("POST", `/api/bookings/public/sites/${site.site_slug}/portal/verify`, {
        body: {
          ...(channel === "phone" ? { phone: identifier.trim() } : { email: identifier.trim() }),
          code: code.trim(),
        },
      });
      const s: PortalSession = {
        token: res.portal_token,
        contactName: res.contact_name,
        expiresAt: Date.now() + res.expires_in * 1000,
      };
      sessionStorage.setItem(sessionKey(site.site_slug), JSON.stringify(s));
      setSession(s);
      setStep("bookings");
    } catch (e) {
      setError(e instanceof Error ? e.message : "Verification failed.");
    } finally {
      setBusy(false);
    }
  };

  const cancelBooking = async (id: string) => {
    if (!session) return;
    setBusy(true);
    setError(null);
    try {
      await portalFetch("POST", `/api/bookings/portal/bookings/${id}/cancel`, {
        token: session.token,
        body: {},
      });
      await loadBookings(session);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Cancel failed.");
    } finally {
      setBusy(false);
    }
  };

  const rescheduleBooking = async (id: string) => {
    if (!session || !newStart) return;
    setBusy(true);
    setError(null);
    try {
      await portalFetch("POST", `/api/bookings/portal/bookings/${id}/reschedule`, {
        token: session.token,
        body: { starts_at: new Date(newStart).toISOString() },
      });
      setRescheduleFor(null);
      setNewStart("");
      await loadBookings(session);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Reschedule failed.");
    } finally {
      setBusy(false);
    }
  };

  const accent = site.theme?.primaryColor ?? site.theme?.accent ?? "#7c5b3e";

  return (
    <div className="min-h-screen bg-background">
      <header className="border-b border-border bg-card">
        <div className="mx-auto max-w-2xl px-6 py-6">
          <h1 className="text-2xl font-bold tracking-tight">{site.business_name}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Manage your bookings — no account needed.
          </p>
        </div>
      </header>

      <main className="mx-auto max-w-2xl px-6 py-8">
        {error ? <ErrorNote message={error} /> : null}

        {step === "request" ? (
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <KeyRound className="h-5 w-5" style={{ color: accent }} />
                Sign in with a code
              </CardTitle>
              <CardDescription>
                Enter the phone number or e-mail you booked with. We&apos;ll send you a
                6-digit login code.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="flex gap-2">
                <Button
                  type="button"
                  variant={channel === "phone" ? "default" : "outline"}
                  onClick={() => setChannel("phone")}
                >
                  Phone
                </Button>
                <Button
                  type="button"
                  variant={channel === "email" ? "default" : "outline"}
                  onClick={() => setChannel("email")}
                >
                  E-mail
                </Button>
              </div>
              <div>
                <Label htmlFor="identifier">
                  {channel === "phone" ? "Phone number" : "E-mail address"}
                </Label>
                <Input
                  id="identifier"
                  type={channel === "phone" ? "tel" : "email"}
                  value={identifier}
                  onChange={(e) => setIdentifier(e.target.value)}
                  placeholder={channel === "phone" ? "+1 555 000 1234" : "you@example.com"}
                />
              </div>
              <Button onClick={requestCode} disabled={busy || identifier.trim().length < 5}>
                {busy ? "Sending…" : "Send login code"}
              </Button>
            </CardContent>
          </Card>
        ) : null}

        {step === "verify" ? (
          <Card>
            <CardHeader>
              <CardTitle>Enter your code</CardTitle>
              <CardDescription>
                We sent a 6-digit code to {identifier}. It is valid for 10 minutes.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div>
                <Label htmlFor="code">6-digit code</Label>
                <Input
                  id="code"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  maxLength={6}
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
                  placeholder="123456"
                />
              </div>
              <div className="flex gap-2">
                <Button onClick={verifyCode} disabled={busy || code.length !== 6}>
                  {busy ? "Verifying…" : "Verify & sign in"}
                </Button>
                <Button type="button" variant="outline" onClick={() => setStep("request")}>
                  Back
                </Button>
              </div>
            </CardContent>
          </Card>
        ) : null}

        {step === "bookings" && session ? (
          <div className="space-y-4">
            <div className="flex items-center justify-between">
              <p className="text-sm text-muted-foreground">
                Signed in as <span className="font-medium text-foreground">{session.contactName}</span>
              </p>
              <div className="flex gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => session && loadBookings(session).catch(() => {})}
                >
                  <RefreshCw className="mr-1 h-4 w-4" /> Refresh
                </Button>
                <Button type="button" variant="outline" size="sm" onClick={logout}>
                  <LogOut className="mr-1 h-4 w-4" /> Sign out
                </Button>
              </div>
            </div>

            {bookings.length === 0 ? (
              <Card>
                <CardContent className="py-10 text-center text-muted-foreground">
                  <CalendarX className="mx-auto mb-2 h-8 w-8" />
                  No bookings found for your contact details.
                </CardContent>
              </Card>
            ) : (
              bookings.map((b) => (
                <Card key={b.id}>
                  <CardContent className="space-y-3 py-4">
                    <div className="flex items-center justify-between">
                      <div>
                        <p className="font-medium">
                          {formatDateTime(b.starts_at, site.locale, site.timezone)}
                        </p>
                        <p className="text-xs text-muted-foreground">
                          #{b.id.slice(0, 8)} · {b.source}
                        </p>
                      </div>
                      <Badge variant={b.status === "cancelled" ? "destructive" : "default"}>
                        {b.status}
                      </Badge>
                    </div>
                    {b.status !== "cancelled" ? (
                      <div className="flex flex-wrap items-center gap-2">
                        {rescheduleFor === b.id ? (
                          <>
                            <Input
                              type="datetime-local"
                              value={newStart}
                              onChange={(e) => setNewStart(e.target.value)}
                              className="w-auto"
                            />
                            <Button size="sm" onClick={() => rescheduleBooking(b.id)} disabled={busy || !newStart}>
                              Confirm
                            </Button>
                            <Button
                              size="sm"
                              variant="outline"
                              onClick={() => {
                                setRescheduleFor(null);
                                setNewStart("");
                              }}
                            >
                              Keep
                            </Button>
                          </>
                        ) : (
                          <>
                            <Button
                              size="sm"
                              variant="outline"
                              onClick={() => setRescheduleFor(b.id)}
                              disabled={busy}
                            >
                              Reschedule
                            </Button>
                            <Button
                              size="sm"
                              variant="destructive"
                              onClick={() => cancelBooking(b.id)}
                              disabled={busy}
                            >
                              Cancel
                            </Button>
                          </>
                        )}
                      </div>
                    ) : null}
                  </CardContent>
                </Card>
              ))
            )}
          </div>
        ) : null}
      </main>
    </div>
  );
}
