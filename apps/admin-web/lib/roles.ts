/**
 * Shared realm-role helpers (SPEC-W7 Part C). The roles come from the
 * Keycloak `realm_access.roles` claim, surfaced on the Auth.js session as
 * `session.realmRoles` (see lib/auth.ts). Use these helpers for both
 * server-side guards (page redirects) and client-side nav/section hiding so
 * the two never drift apart.
 */

/** Keycloak realm roles understood by the dashboard. */
export type RealmRole =
  | "owner"
  | "admin"
  | "staff"
  | "viewer"
  | "analyst"
  | "billing";

/** Analytics dashboard (KPIs, trends, text-to-SQL) — read-only. */
export const ANALYTICS_ROLES: readonly RealmRole[] = [
  "owner",
  "admin",
  "analyst",
];

/** Billing section (invoices, rate cards, payouts, QR payment links). */
export const BILLING_ROLES: readonly RealmRole[] = ["owner", "billing"];

export function hasAnyRole(
  roles: readonly string[] | undefined | null,
  allowed: readonly RealmRole[],
): boolean {
  if (!roles) return false;
  return allowed.some((r) => roles.includes(r));
}

/** owner/admin/analyst — staff and viewer never see analytics. */
export function canViewAnalytics(
  roles: readonly string[] | undefined | null,
): boolean {
  return hasAnyRole(roles, ANALYTICS_ROLES);
}

/** owner/billing — staff, viewer and analyst never see billing. */
export function canViewBilling(
  roles: readonly string[] | undefined | null,
): boolean {
  return hasAnyRole(roles, BILLING_ROLES);
}
