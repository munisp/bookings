package bookingops

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/daprc"
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

// TenantResolver resolves tenant slugs to IDs/context via Dapr service
// invocation to identity-service, with a small in-memory cache.
type TenantResolver struct {
	dapr  *daprc.Client
	appID string

	mu    sync.Mutex
	cache map[string]TenantInfo
}

// NewTenantResolver builds the resolver.
func NewTenantResolver(d *daprc.Client, identityAppID string) *TenantResolver {
	return &TenantResolver{dapr: d, appID: identityAppID, cache: map[string]TenantInfo{}}
}

// BySlug resolves (and caches) a tenant by slug.
func (r *TenantResolver) BySlug(ctx context.Context, slug string) (TenantInfo, error) {
	r.mu.Lock()
	if t, ok := r.cache[slug]; ok {
		r.mu.Unlock()
		return t, nil
	}
	r.mu.Unlock()

	var t TenantInfo
	if err := r.dapr.InvokeService(ctx, r.appID, "v1/tenants/"+slug, nil, &t); err != nil {
		return TenantInfo{}, fmt.Errorf("resolve tenant %q: %w", slug, err)
	}
	if t.ID == uuid.Nil {
		return TenantInfo{}, fmt.Errorf("resolve tenant %q: empty id", slug)
	}
	t.Slug = slug
	r.mu.Lock()
	r.cache[slug] = t
	r.mu.Unlock()
	return t, nil
}
