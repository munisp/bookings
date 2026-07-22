package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/opendesk/identity-service/internal/store"
	"go.uber.org/zap"
)

// Digital twins (SPEC-W3 §3 innovation 12): a twin is an ephemeral copy of a
// tenant used for safe what-if experiments and demos. Twins are created via
// POST /internal/tenants/{slug}/twin, onboarded exactly like a real tenant
// (same TenantOnboardingWorkflow → site seed, search alias, industry pack),
// carry plan='twin' and metadata {"twin_of": "<source slug>"}, and are
// deleted after 24h by notification-worker's TwinCleanupWorkflow.
//
// Deletion guard (DELETE /v1/tenants/{slug}): permify-free by design —
// twin tenants (slug contains "-twin-") may always be deleted (the cleanup
// workflow authenticates via the private Dapr mesh, and operators via the
// admin UI); any other slug requires the caller to hold the manage_catalog
// permission on the organization (Permify check on the JWT sub / X-User-Id).

// twinSlugMarker identifies digital-twin tenants.
const twinSlugMarker = "-twin-"

// twinRandAlphabet for the 6-char random suffix (DNS-safe, unambiguous).
const twinRandAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// createTwin handles POST /internal/tenants/{slug}/twin.
func (s *server) createTwin(w http.ResponseWriter, r *http.Request) {
	src, err := s.tenant(w, r)
	if err != nil {
		return
	}
	slug, err := newTwinSlug(src.Slug)
	if err != nil {
		s.internal(w, err)
		return
	}
	metadata, _ := json.Marshal(map[string]string{"twin_of": src.Slug})
	t := store.Tenant{
		Slug:        slug,
		Name:        src.Name + " (twin)",
		Timezone:    src.Timezone,
		Currency:    src.Currency,
		Locale:      src.Locale,
		Terminology: src.Terminology,
		Plan:        "twin",
		Industry:    src.Industry, // industry copied from the source tenant
		Metadata:    metadata,
	}
	if err := s.d.Store.CreateTenant(r.Context(), &t); err != nil {
		s.internal(w, err)
		return
	}

	// Onboard exactly like createTenant (durable workflow seeds site, search
	// alias, pack) and arm the 24h cleanup — both fire-and-forget.
	go s.triggerOnboarding(t)
	go s.triggerTwinCleanup(t, src.Slug)

	s.d.Logger.Info("digital twin created",
		zap.String("slug", t.Slug), zap.String("twin_of", src.Slug))
	writeJSON(w, http.StatusCreated, t)
}

// newTwinSlug builds "{slug}-twin-{6rand}", truncating the base so the
// result always satisfies slugRe (≤63 chars).
func newTwinSlug(base string) (string, error) {
	const suffix = 12 // len("-twin-") + 6 random chars
	if len(base)+suffix > 63 {
		base = strings.TrimRight(base[:63-suffix], "-")
	}
	rnd := make([]byte, 6)
	if _, err := rand.Read(rnd); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(base)
	b.WriteString(twinSlugMarker)
	for _, c := range rnd {
		b.WriteByte(twinRandAlphabet[int(c)%len(twinRandAlphabet)])
	}
	return b.String(), nil
}

// triggerTwinCleanup asks notification-worker to start TwinCleanupWorkflow
// (24h timer → Dapr DELETE /v1/tenants/{slug}) via Dapr service invocation.
func (s *server) triggerTwinCleanup(t store.Tenant, twinOf string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := s.d.Dapr.InvokeService(ctx, s.d.NotificationAppID, "dev/trigger-twin-cleanup", map[string]any{
		"tenant_id": t.ID.String(),
		"slug":      t.Slug,
		"twin_of":   twinOf,
	}, nil)
	if err != nil {
		s.d.Logger.Error("failed to trigger TwinCleanupWorkflow",
			zap.String("slug", t.Slug), zap.Error(err))
		return
	}
	s.d.Logger.Info("TwinCleanupWorkflow triggered", zap.String("slug", t.Slug))
}

// deleteTenant handles DELETE /v1/tenants/{slug} with the twin/permify guard
// documented above.
func (s *server) deleteTenant(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if !strings.Contains(slug, twinSlugMarker) {
		// Non-twin deletion requires manage_catalog on the organization.
		t, err := s.d.Store.GetTenantBySlug(r.Context(), slug)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		if err != nil {
			s.internal(w, err)
			return
		}
		userID := bearerSubject(r.Header.Get("Authorization"))
		if userID == "" {
			userID = r.Header.Get("X-User-Id")
		}
		if userID == "" {
			writeError(w, http.StatusUnauthorized, "authenticated subject required (JWT sub or X-User-Id)")
			return
		}
		allowed, err := s.d.Permify.Check(r.Context(), t.ID.String(),
			"user:"+userID, "manage_catalog", "organization:"+t.ID.String())
		if err != nil {
			s.d.Logger.Error("permify check failed", zap.Error(err))
			writeError(w, http.StatusBadGateway, "authorization service error")
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "missing permission manage_catalog (only twin tenants delete freely)")
			return
		}
	}
	if err := s.d.Store.DeleteTenant(r.Context(), slug); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		s.internal(w, err)
		return
	}
	s.d.Logger.Info("tenant deleted", zap.String("slug", slug))
	writeJSON(w, http.StatusOK, map[string]string{"deleted": slug})
}

// bearerSubject extracts the JWT sub claim without verifying the signature —
// the same trust model as booking-service's parseBearerClaims (the gateway
// terminates OIDC; internal callers are trusted by network policy).
func bearerSubject(header string) string {
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(header, "Bearer "), ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Sub
}
