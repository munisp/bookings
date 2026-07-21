/**
 * Terminology resolution (SPEC-CRM §D).
 *
 * Dashboard copy is resolved with the precedence:
 *   tenant.terminology overrides  >  pack.dashboardLabels  >  built-in defaults
 *
 * The tenant's industry pack is embedded in the identity-service tenant
 * response (`tenant.pack.dashboardLabels`); older responses without a pack
 * simply fall through to the defaults.
 */

import type { Tenant } from "./types";

export interface BookingLabels {
  /** e.g. "Booking" / "Reservation" / "Ticket" */
  bookingSingular: string;
  /** e.g. "Bookings" / "Reservations" / "Tickets" */
  bookingPlural: string;
  /** e.g. "Guest" / "Patient" / "Client" */
  customerTerm: string;
}

export const DEFAULT_BOOKING_LABELS: BookingLabels = {
  bookingSingular: "Booking",
  bookingPlural: "Bookings",
  customerTerm: "Guest",
};

export function resolveBookingLabels(
  tenant: Pick<Tenant, "terminology" | "pack"> | null | undefined,
): BookingLabels {
  const term = tenant?.terminology ?? {};
  const packLabels = tenant?.pack?.dashboardLabels ?? {};

  const singular =
    term.booking ?? packLabels.bookingSingular ?? DEFAULT_BOOKING_LABELS.bookingSingular;

  return {
    bookingSingular: singular,
    bookingPlural:
      term.bookings ?? packLabels.bookingPlural ?? `${singular}s`,
    customerTerm:
      term.contact ??
      term.customer ??
      packLabels.customerTerm ??
      DEFAULT_BOOKING_LABELS.customerTerm,
  };
}
