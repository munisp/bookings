package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/store"
)

// ---------------------------------------------------------------------------
// Tenant-scoped site management (web app: GET/PUT /v1/site)
// ---------------------------------------------------------------------------

// getSite (GET /v1/site) returns the tenant's public site, auto-creating a
// default one (slug = tenant slug) when none exists yet.
func (s *server) getSite(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	site, err := s.d.Store.GetSiteByTenant(r.Context(), tenant.ID)
	if errors.Is(err, store.ErrNotFound) {
		site = store.Site{
			TenantID:    tenant.ID,
			TenantSlug:  tenant.Slug,
			Slug:        tenant.Slug,
			DisplayName: tenant.Name,
			Theme:       json.RawMessage(`{}`),
		}
		if cerr := s.d.Store.CreateSite(r.Context(), &site); cerr != nil {
			s.internal(w, cerr)
			return
		}
	} else if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, site)
}

type updateSiteRequest struct {
	DisplayName *string         `json:"display_name,omitempty"`
	Published   *bool           `json:"published,omitempty"`
	Theme       json.RawMessage `json:"theme,omitempty"`
}

// updateSite (PUT /v1/site, manage_catalog) updates display_name, published
// and theme. The slug is immutable.
func (s *server) updateSite(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	var req updateSiteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Theme) > 0 && !json.Valid(req.Theme) {
		writeError(w, http.StatusBadRequest, "theme must be valid JSON")
		return
	}
	site, err := s.d.Store.GetSiteByTenant(r.Context(), tenant.ID)
	if errors.Is(err, store.ErrNotFound) {
		// create-then-update: same defaulting behavior as GET /v1/site
		site = store.Site{
			TenantID:    tenant.ID,
			TenantSlug:  tenant.Slug,
			Slug:        tenant.Slug,
			DisplayName: tenant.Name,
			Theme:       json.RawMessage(`{}`),
		}
		if cerr := s.d.Store.CreateSite(r.Context(), &site); cerr != nil {
			s.internal(w, cerr)
			return
		}
	} else if err != nil {
		s.internal(w, err)
		return
	}

	if req.DisplayName != nil {
		site.DisplayName = *req.DisplayName
	}
	if req.Published != nil {
		site.Published = *req.Published
	}
	if len(req.Theme) > 0 {
		site.Theme = req.Theme
	}
	if err := s.d.Store.UpdateSite(r.Context(), &site); err != nil {
		s.mapOpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, site)
}

type createSiteRequest struct {
	TenantID    string `json:"tenant_id"`
	TenantSlug  string `json:"tenant_slug"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

// createSiteInternal (POST /internal/sites) seeds/updates the public booking
// page for a tenant. Invoked by the TenantOnboardingWorkflow via Dapr.
func (s *server) createSiteInternal(w http.ResponseWriter, r *http.Request) {
	var req createSiteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil || req.Slug == "" || req.TenantSlug == "" {
		writeError(w, http.StatusBadRequest, "tenant_id (uuid), tenant_slug and slug are required")
		return
	}
	site := store.Site{
		TenantID:    tenantID,
		TenantSlug:  req.TenantSlug,
		Slug:        req.Slug,
		DisplayName: req.DisplayName,
	}
	if err := s.d.Store.CreateSite(r.Context(), &site); err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, site)
}
