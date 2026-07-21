/**
 * Domain models mirrored from the platform services (see SPEC.md §7).
 * Field names match the Postgres schemas / JSON contracts emitted by the
 * Go and Python services behind the APISIX gateway.
 */

// ---------- identity-service ----------

export interface Tenant {
  id: string;
  slug: string;
  name: string;
  timezone: string;
  currency: string;
  locale: string;
  /** Per-tenant vocabulary overrides, e.g. { "booking": "reservation" }. */
  terminology: Record<string, string>;
  plan: "free" | "pro" | "business" | string;
  /** Industry pack id, e.g. "salon" | "clinic" | "consultancy" | "support-desk". */
  industry?: string;
  /** Resolved industry pack summary (absent on older identity-service builds). */
  pack?: IndustryPackSummary | null;
  created_at: string;
}

/** Booking policy carried by an industry pack (SPEC-CRM §C). */
export interface BookingPolicy {
  depositPercent?: number;
  noShowFeeCents?: number;
  phoneConfirmation?: boolean;
  intakeRequired?: boolean;
  cancellationWindowHours?: number;
}

/** Dashboard copy provided by an industry pack. */
export interface DashboardLabels {
  bookingSingular?: string;
  bookingPlural?: string;
  customerTerm?: string;
}

/** Resolved pack summary embedded in GET /v1/tenants/{slug}. */
export interface IndustryPackSummary {
  displayName?: string;
  terminology?: Record<string, string>;
  bookingPolicy?: BookingPolicy;
  dashboardLabels?: DashboardLabels;
}

export interface PublicSite {
  tenant_slug: string;
  site_slug: string;
  published: boolean;
  theme: SiteTheme;
  business_name: string;
  tagline?: string;
  timezone: string;
  currency: string;
  locale: string;
}

export interface SiteTheme {
  /** hex colour, e.g. "#8a6d4b" (legacy snake_case keys kept for compatibility) */
  accent?: string;
  logo_url?: string;
  hero_blurb?: string;
  /** Wave-3 theme editor contract (PUT /v1/site theme jsonb, camelCase). */
  primaryColor?: string;
  logoUrl?: string;
  heroTitle?: string;
  heroSubtitle?: string;
  /** layout template id, e.g. "classic" | "modern" | "compact" */
  template?: string;
}

// ---------- booking-service ----------

export type BookingStatus =
  | "pending"
  | "confirmed"
  | "rescheduled"
  | "cancelled"
  | "completed"
  | "no_show";

export type BookingSource = "voice" | "chat" | "web" | "admin" | string;

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
}

export interface TeamMember {
  id: string;
  tenant_id: string;
  name: string;
  email: string;
  role: string;
  active: boolean;
}

export interface AvailabilityRule {
  id: string;
  tenant_id: string;
  team_member_id: string;
  /** 0 = Sunday .. 6 = Saturday */
  weekday: number;
  /** minutes after midnight, local tenant time */
  start_min: number;
  end_min: number;
  effective_from?: string | null;
  effective_to?: string | null;
}

export interface Contact {
  id: string;
  tenant_id: string;
  name: string;
  phone: string;
  email: string;
  notes: string;
}

export interface Booking {
  id: string;
  tenant_id: string;
  offering_id: string;
  team_member_id: string | null;
  contact_id: string;
  starts_at: string;
  ends_at: string;
  status: BookingStatus;
  source: BookingSource;
  idempotency_key: string;
  created_at: string;
  updated_at: string;
  /** Joined/enriched fields the service may include in list responses. */
  offering_name?: string;
  contact_name?: string;
  contact_phone?: string;
  team_member_name?: string;
}

export interface AvailabilitySlot {
  starts_at: string;
  ends_at: string;
  team_member_id?: string | null;
  remaining_capacity?: number;
}

// ---------- knowledge-service ----------

export interface KnowledgeDocument {
  id: string;
  tenant_id: string;
  title: string;
  body: string;
  source_url?: string | null;
  created_at: string;
}

export interface KnowledgeSearchHit {
  document_id: string;
  chunk_id?: string;
  title: string;
  snippet: string;
  score: number;
}

/**
 * Self-improving KB draft (knowledge-service, innovation 4): created when a
 * search scores below the suggestion threshold and the query looks like a
 * question. Approve creates a real document; reject deletes the suggestion.
 */
export interface KbSuggestion {
  id: string;
  tenant_id: string;
  /** the customer question that fell below the score threshold */
  question: string;
  suggested_title?: string | null;
  suggested_answer?: string | null;
  /** best RRF score seen for the question (for context) */
  score?: number | null;
  status: "pending" | "approved" | "rejected" | string;
  created_at: string;
}

/** Text-to-SQL analytics result (knowledge-service POST /v1/analytics/query). */
export interface AnalyticsQueryResult {
  sql: string;
  columns: string[];
  /** rows as positional arrays aligned with `columns` */
  rows: unknown[][];
  truncated?: boolean;
  explanation?: string;
}

// ---------- analytics-pipeline (revenue intelligence) ----------

/** Pricing recommendation (analytics-pipeline GET /v1/recommendations). */
export interface PricingRecommendation {
  tenant_id: string;
  offering_id: string;
  offering_name?: string;
  peak_hour_multiplier: number;
  suggested_deposit_pct: number;
  no_show_risk_band?: string;
  generated_at?: string;
}

// ---------- payments-service ----------

export interface AccountBalance {
  tenant_id: string;
  /** pending deposit holds */
  deposits_cents: number;
  /** captured, pay-out-able revenue */
  revenue_cents: number;
  /** lifetime paid out */
  paid_out_cents: number;
  currency: string;
}

export interface LedgerEntry {
  id: string;
  tenant_id: string;
  /** TigerBeetle transfer code: 100 hold, 101 capture, 102 refund, 103 no-show fee, 104 payout */
  code: number;
  amount_cents: number;
  currency: string;
  booking_id?: string | null;
  memo?: string;
  created_at: string;
}

export interface Payout {
  id: string;
  tenant_id: string;
  amount_cents: number;
  currency: string;
  status: "requested" | "settled" | "failed" | string;
  /** Mojaloop transfer reference when settled cross-border. */
  rail_ref?: string | null;
  created_at: string;
}

// ---------- conversation / voice ----------

export interface ChatRequest {
  tenant: string;
  message: string;
  conversation_id?: string;
  /** site slug when the chat originates from a public booking page */
  site_slug?: string;
}

export interface ChatResponse {
  conversation_id: string;
  reply: string;
  tool_calls?: { name: string; result: unknown }[];
}

export interface VoiceSession {
  /** LiveKit access token for the browser client */
  token: string;
  /** LiveKit websocket URL, e.g. ws://localhost:7880 */
  url: string;
  room: string;
}

// ---------- gateway-edge WebSocket events ----------

export interface BookingEvent {
  type:
    | "BookingCreated"
    | "BookingConfirmed"
    | "BookingRescheduled"
    | "BookingCancelled"
    | "BookingNoShow"
    | string;
  tenant_id: string;
  booking: Booking;
  time: string;
}

/**
 * Warm-handoff escalation (voice runtime, innovation 1): the receptionist
 * opened a LiveKit room and asks for a human. Fanned out on the /ws channel.
 */
export interface EscalationRequestedEvent {
  type: "EscalationRequested";
  data: {
    conversation_id: string;
    room: string;
    join_token_staff: string;
    site_slug?: string;
  };
  tenant_id?: string;
  time?: string;
}

export type WsEvent = BookingEvent | EscalationRequestedEvent;

// ---------- generic envelopes ----------

export interface ListResponse<T> {
  items: T[];
  total?: number;
}

export interface ApiProblem {
  error: string;
  message?: string;
  status?: number;
}
