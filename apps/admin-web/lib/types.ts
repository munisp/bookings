/** Shared API payload types for the admin web app. */

export interface Tenant {
  id: string;
  slug: string;
  name: string;
  plan: string;
  timezone: string;
  currency: string;
  locale: string;
  industry: string;
  pack: IndustryPack | null;
  created_at: string;
}

/** Industry pack summary returned on GET /v1/tenants/{slug} (SPEC-CRM §C3). */
export interface IndustryPack {
  id: string;
  displayName: string;
  terminology: Record<string, string>;
  bookingPolicy: BookingPolicy;
  dashboardLabels: Record<string, string>;
  agentPersona: string;
  temporalWorkflow: string;
}

export interface BookingPolicy {
  depositPercent: number;
  noShowFeeCents: number;
  phoneConfirmation: boolean;
  intakeRequired: boolean;
  cancellationWindowHours: number;
}

export interface Offering {
  id: string;
  tenant_id: string;
  name: string;
  description: string;
  duration_min: number;
  buffer_min: number;
  price_cents: number;
  currency: string;
  capacity: number;
  bookable: boolean;
  created_at: string;
  updated_at: string;
}

export interface TeamMember {
  id: string;
  tenant_id: string;
  name: string;
  email: string;
  role: string;
  active: boolean;
  created_at: string;
  updated_at: string;
}

export interface AvailabilityRule {
  id: string;
  tenant_id: string;
  team_member_id: string;
  weekday: number; // 0=Sunday
  start_min: number;
  end_min: number;
  effective_from: string; // RFC3339
  effective_to: string; // RFC3339
}

export interface Booking {
  id: string;
  tenant_id: string;
  offering_id: string;
  team_member_id: string;
  contact_id: string;
  starts_at: string;
  ends_at: string;
  status: "pending" | "confirmed" | "cancelled" | "no_show" | "completed";
  source: string;
  idempotency_key: string;
  created_at: string;
  updated_at: string;
}

export interface Contact {
  id: string;
  tenant_id: string;
  name: string;
  phone: string;
  email: string;
  notes: string;
  source?: string;
  external_id?: string;
  created_at: string;
  updated_at: string;
}

export interface Slot {
  starts_at: string;
  ends_at: string;
}

/** One slot annotated with its fragmentation score (GET ?optimize=true, Wave 5 #4). */
export interface ScoredSlot extends Slot {
  score: number;
  reason: string;
}

/** GET /v1/availability/optimize — top 3 suggestions minimizing fragmentation. */
export interface OptimizedAvailability {
  offering_id: string;
  team_member_id: string;
  date: string;
  suggestions: ScoredSlot[];
}

export interface AvailabilityResponse {
  offering_id: string;
  team_member_id: string;
  slots: Slot[] | ScoredSlot[];
  optimized?: boolean;
}

export interface Site {
  id: string;
  tenant_id: string;
  tenant_slug: string;
  slug: string;
  display_name: string;
  published: boolean;
  theme: Record<string, unknown>;
  created_at: string;
}

export interface PublicContext {
  site: {
    slug: string;
    display_name: string;
    tenant_id: string;
    tenant_slug: string;
    theme: Record<string, unknown>;
  };
  tenant: {
    id?: string;
    slug?: string;
    name: string;
    timezone: string;
    currency: string;
    locale: string;
    terminology: Record<string, string>;
    industry?: string;
    pack?: IndustryPack | null;
  };
  offerings: Offering[];
  team_members: TeamMember[];
}

/** Waitlist entry (SPEC-W3 §3 innovation 7). */
export interface WaitlistEntry {
  id: string;
  tenant_id: string;
  offering_id: string;
  team_member_id?: string;
  contact_name: string;
  contact_phone: string;
  contact_email: string;
  preferred_from: string;
  preferred_to: string;
  status: "open" | "notified" | "claimed" | "expired";
  claim_token_hash?: string;
  claim_expires_at?: string;
  notified_at?: string;
  created_at: string;
  updated_at: string;
}

/** Outbound webhook subscription (Wave 5 #10, notification-worker). */
export interface WebhookSubscription {
  id: string;
  tenant_id?: string;
  url: string;
  events: string[];
  active: boolean;
  secret_set?: boolean;
  /** Plaintext secret — present ONLY in the POST /v1/webhooks response. */
  secret?: string;
  created_at: string;
}

/** Outbound webhook delivery record (Wave 5 #10). */
export interface WebhookDelivery {
  id: string;
  sub_id: string;
  event_type: string;
  status: "pending" | "retrying" | "delivered" | "dlq";
  attempts: number;
  last_status_code?: number;
  next_retry_at?: string;
  created_at: string;
  updated_at: string;
}
