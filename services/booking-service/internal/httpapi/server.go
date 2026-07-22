// Package httpapi wires the chi router, tenant/auth middleware and REST
// handlers for booking-service.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/bookingops"
	"github.com/opendesk/booking-service/internal/cache"
	"github.com/opendesk/booking-service/internal/daprc"
	"github.com/opendesk/booking-service/internal/permify"
	"github.com/opendesk/booking-service/internal/store"
	"go.uber.org/zap"
)

// Deps bundles server dependencies.
type Deps struct {
	Store         *store.Store
	Ops           *bookingops.Service
	Resolver      *bookingops.TenantResolver
	Authz         permify.Authorizer
	AuthzDisabled bool // dev escape hatch (AUTHZ_DISABLED=true)
	Dapr          *daprc.Client
	IdentityAppID string
	Gdpr          GdprStarter // may be nil when Temporal is unreachable
	Cache         *cache.Cache // availability cache; nil disables caching
	Logger        *zap.Logger
}

type ctxKey string

const (
	ctxTenant ctxKey = "tenant"
	ctxUser   ctxKey = "user"
)

// NewRouter builds the chi router with all routes.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	s := &server{d: d}

	r.Get("/healthz", s.healthz)

	// Tenant-scoped management API (SPEC §8: tenant from JWT claim or
	// X-Tenant-Slug header, validated by middleware).
	r.Route("/v1", func(r chi.Router) {
		r.Use(s.tenantMiddleware)
		r.Route("/offerings", func(r chi.Router) {
			r.Get("/", s.listOfferings)
			r.With(s.require("manage_catalog")).Post("/", s.createOffering)
			r.Get("/{id}", s.getOffering)
			r.With(s.require("manage_catalog")).Put("/{id}", s.updateOffering)
			r.With(s.require("manage_catalog")).Delete("/{id}", s.deleteOffering)
		})
		r.Route("/team-members", func(r chi.Router) {
			r.Get("/", s.listTeamMembers)
			r.With(s.require("manage_catalog")).Post("/", s.createTeamMember)
			r.Get("/{id}", s.getTeamMember)
			r.With(s.require("manage_catalog")).Put("/{id}", s.updateTeamMember)
			r.With(s.require("manage_catalog")).Delete("/{id}", s.deleteTeamMember)
			r.With(s.require("manage_catalog")).Put("/{id}/availability", s.putAvailability)
			r.Get("/{id}/availability", s.getAvailabilityRules)
		})
		r.Get("/availability", s.getAvailability)
		r.Get("/site", s.getSite)
		r.With(s.require("manage_catalog")).Put("/site", s.updateSite)
		r.Route("/contacts", func(r chi.Router) {
			r.Get("/", s.listContacts)
			r.With(s.require("manage_bookings")).Post("/", s.createContact)
			r.Get("/{id}", s.getContact)
			r.With(s.require("manage_bookings")).Put("/{id}", s.updateContact)
			r.With(s.require("manage_bookings")).Delete("/{id}", s.deleteContact)
		})
		r.Route("/bookings", func(r chi.Router) {
			r.With(s.require("manage_bookings")).Post("/", s.createBooking)
			r.Get("/", s.listBookings)
			r.Get("/{id}", s.getBooking)
			r.With(s.require("manage_bookings")).Post("/{id}/reschedule", s.rescheduleBooking)
			r.With(s.require("manage_bookings")).Post("/{id}/cancel", s.cancelBooking)
		})
		r.Route("/waitlist", func(r chi.Router) {
			r.With(s.require("manage_bookings")).Post("/", s.createWaitlistEntry)
			r.Get("/", s.listWaitlist)
			// Claim is token-authorized (capability), not permify-guarded —
			// the claimant is an end customer following the backfill link.
			r.Post("/{id}/claim", s.claimWaitlist)
		})
		// GDPR privacy endpoints (SPEC-W3 §2 innovation 13) — restored
		// additively; handlers live in privacy.go.
		r.Route("/privacy", func(r chi.Router) {
			r.With(s.require("manage_bookings")).Post("/export", s.gdprExport)
			r.With(s.require("manage_bookings")).Post("/erase", s.gdprErase)
		})
	})

	// Public booking page endpoints — no auth; the site slug resolves the
	// tenant server-side, so cross-tenant access is impossible by construction.
	r.Route("/public/sites/{slug}", func(r chi.Router) {
		// Widget/page shell endpoints (no auth, published sites only).
		r.Get("/", s.publicSite)
		r.Get("/offerings", s.publicOfferings)
		r.Get("/context", s.publicContext)
		r.Get("/availability", s.publicAvailability)
		r.Post("/bookings", s.publicCreateBooking)
	})

	// Temporal activity endpoints invoked by the saga workers via Dapr
	// service invocation (SPEC §6).
	r.Route("/activities", func(r chi.Router) {
		r.Post("/reserve-slot", s.activityReserveSlot)
		r.Post("/confirm-booking", s.activityConfirmBooking)
		r.Post("/release-slot", s.activityReleaseSlot)
		r.Post("/mark-no-show", s.activityMarkNoShow)
	})

	// Internal endpoints invoked by other services via Dapr (e.g. the
	// TenantOnboardingWorkflow seeds the default public site).
	r.Post("/internal/sites", s.createSiteInternal)

	return r
}

type server struct{ d Deps }

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.d.Store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "db unreachable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// tenantMiddleware resolves the tenant from the X-Tenant-Slug header (or JWT
// tenant claim) via identity-service and enforces JWT tenant membership when
// the token carries the tenant_slugs claim (SPEC §8).
func (s *server) tenantMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := parseBearerClaims(r.Header.Get("Authorization"))

		slug := r.Header.Get("X-Tenant-Slug")
		if slug == "" {
			slug = claims.firstTenant()
		}
		if slug == "" {
			writeError(w, http.StatusBadRequest, "X-Tenant-Slug header (or JWT tenant_slugs claim) is required")
			return
		}
		// If the token enumerates tenants, the requested one must be among them.
		if len(claims.TenantSlugs) > 0 && !claims.hasTenant(slug) {
			writeError(w, http.StatusForbidden, "token not entitled to tenant "+slug)
			return
		}
		tenant, err := s.d.Resolver.BySlug(r.Context(), slug)
		if err != nil {
			s.d.Logger.Warn("tenant resolution failed", zap.String("slug", slug), zap.Error(err))
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		ctx := context.WithValue(r.Context(), ctxTenant, tenant)
		ctx = context.WithValue(ctx, ctxUser, claims.Sub)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// require returns middleware enforcing a Permify permission
// (manage_catalog / manage_bookings) on organization:{tenantID} for the JWT
// subject, per SPEC §8.
func (s *server) require(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.d.AuthzDisabled {
				next.ServeHTTP(w, r)
				return
			}
			tenant := tenantFrom(r.Context())
			userID := userFrom(r.Context())
			if userID == "" {
				writeError(w, http.StatusUnauthorized, "authenticated subject required (JWT sub or X-User-Id)")
				return
			}
			allowed, err := s.d.Authz.Check(r.Context(), tenant.ID.String(),
				"user:"+userID, permission, "organization:"+tenant.ID.String())
			if err != nil {
				s.d.Logger.Error("permify check failed", zap.Error(err))
				writeError(w, http.StatusBadGateway, "authorization service error")
				return
			}
			if !allowed {
				writeError(w, http.StatusForbidden, "missing permission "+permission)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func tenantFrom(ctx context.Context) bookingops.TenantInfo {
	t, _ := ctx.Value(ctxTenant).(bookingops.TenantInfo)
	return t
}

func userFrom(ctx context.Context) string {
	u, _ := ctx.Value(ctxUser).(string)
	return u
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *server) internal(w http.ResponseWriter, err error) {
	s.d.Logger.Error("internal error", zap.Error(err))
	writeError(w, http.StatusInternalServerError, "internal error")
}

// mapOpError converts bookingops/store sentinel errors to HTTP statuses.
func (s *server) mapOpError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict")
	case errors.Is(err, bookingops.ErrPhoneRequired):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, bookingops.ErrSlotUnavailable):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, bookingops.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.internal(w, err)
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// decodeOptionalJSON decodes a body that may be empty (e.g. POST with no payload).
func decodeOptionalJSON(r *http.Request, v any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	return json.NewDecoder(r.Body).Decode(v)
}

func urlUUID(w http.ResponseWriter, r *http.Request, param string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, param))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid "+param)
		return uuid.Nil, false
	}
	return id, true
}
