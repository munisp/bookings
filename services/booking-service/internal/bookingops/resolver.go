package bookingops

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/daprc"
	"go.uber.org/zap"
)

// TenantInfo is the tenant context resolved from identity-service.
type TenantInfo struct {
	ID       uuid.UUID `json:"id"`
	Slug     string    `json:"slug"`
	Name     string    `json:"name"`
	Timezone string    `json:"timezone"`
	Currency string    `json:"currency"`
	Locale   string    `json:"locale"`
	Plan     string    `json:"plan"`
	// SPEC-CRM §C3: industry pack id + resolved pack summary (absent for
	// tenants created before packs existed or when no pack is loaded).
	Industry string       `json:"industry"`
	Pack     *PackSummary `json:"pack"`
}

// PackSummary mirrors identity-service's pack projection (only the fields
// booking-service consumes are typed; terminology/dashboardLabels/agentPersona
// are passed through for other consumers).
type PackSummary struct {
	ID               string            `json:"id"`
	DisplayName      string            `json:"displayName"`
	Terminology      map[string]string `json:"terminology"`
	BookingPolicy    BookingPolicy     `json:"bookingPolicy"`
	DashboardLabels  map[string]string `json:"dashboardLabels"`
	AgentPersona     string            `json:"agentPersona"`
	TemporalWorkflow string            `json:"temporalWorkflow"`
}

// BookingPolicy mirrors the pack bookingPolicy block.
type BookingPolicy struct {
	DepositPercent          int   `json:"depositPercent"`
	NoShowFeeCents          int64 `json:"noShowFeeCents"`
	PhoneConfirmation       bool  `json:"phoneConfirmation"`
	IntakeRequired          bool  `json:"intakeRequired"`
	CancellationWindowHours int   `json:"cancellationWindowHours"`
}

// Location returns the tenant's IANA timezone (UTC fallback).
func (t TenantInfo) Location() *time.Location {
	loc, err := time.LoadLocation(t.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// DefaultTenantCacheTTL is used when TENANT_CACHE_TTL_SECONDS is unset.
const DefaultTenantCacheTTL = 5 * time.Minute

// TenantResolver resolves tenant slugs to IDs/context via Dapr service
// invocation to identity-service, with an in-memory TTL cache.
//
// Resilience (Wave 5 #5): a cached entry is served until its TTL expires;
// when identity-service then times out or errors on refresh, the EXPIRED
// entry is served stale (logged) rather than failing every tenant-scoped
// request. A tenant that was never resolved successfully still errors.
type TenantResolver struct {
	dapr  *daprc.Client
	appID string
	ttl   time.Duration
	log   *zap.Logger

	mu    sync.Mutex
	cache map[string]tenantCacheEntry
}

type tenantCacheEntry struct {
	info      TenantInfo
	fetchedAt time.Time
}

// NewTenantResolver builds the resolver. ttl <= 0 falls back to
// DefaultTenantCacheTTL.
func NewTenantResolver(d *daprc.Client, identityAppID string, ttl time.Duration, log *zap.Logger) *TenantResolver {
	if ttl <= 0 {
		ttl = DefaultTenantCacheTTL
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &TenantResolver{dapr: d, appID: identityAppID, ttl: ttl, log: log, cache: map[string]tenantCacheEntry{}}
}

// BySlug resolves (and caches) a tenant by slug.
func (r *TenantResolver) BySlug(ctx context.Context, slug string) (TenantInfo, error) {
	r.mu.Lock()
	entry, cached := r.cache[slug]
	fresh := cached && time.Since(entry.fetchedAt) < r.ttl
	r.mu.Unlock()
	if fresh {
		return entry.info, nil
	}

	var t TenantInfo
	if err := r.dapr.InvokeService(ctx, r.appID, "v1/tenants/"+slug, nil, &t); err != nil {
		if cached {
			// identity-service timeout/outage: serve the expired entry
			// stale instead of failing every request for this tenant.
			r.log.Warn("identity-service unreachable; serving stale tenant context",
				zap.String("slug", slug), zap.Duration("age", time.Since(entry.fetchedAt)), zap.Error(err))
			return entry.info, nil
		}
		return TenantInfo{}, fmt.Errorf("resolve tenant %q: %w", slug, err)
	}
	if t.ID == uuid.Nil {
		return TenantInfo{}, fmt.Errorf("resolve tenant %q: empty id", slug)
	}
	t.Slug = slug
	r.mu.Lock()
	r.cache[slug] = tenantCacheEntry{info: t, fetchedAt: time.Now()}
	r.mu.Unlock()
	return t, nil
}
