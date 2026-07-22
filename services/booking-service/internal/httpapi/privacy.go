package httpapi

import (
	"context"
	"net/http"

	"github.com/opendesk/booking-service/internal/temporalclient"
)

// GdprStarter starts the GDPR workflows hosted by notification-worker
// (SPEC-W3 §2 innovation 13). Implemented by temporalclient.Client.
type GdprStarter interface {
	StartGdprExport(ctx context.Context, in temporalclient.GdprRequest) (string, error)
	StartGdprErase(ctx context.Context, in temporalclient.GdprRequest) (string, error)
}

// privacyRequest is the payload of POST /v1/privacy/{export,erase}. At least
// one of phone/email identifies the data subject.
type privacyRequest struct {
	Phone string `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`
}

// gdprExport handles POST /v1/privacy/export (manage_bookings): starts
// GdprExportWorkflow which gathers the subject's data across services into a
// JSON bundle in MinIO bucket `exports` and returns the object path.
func (s *server) gdprExport(w http.ResponseWriter, r *http.Request) {
	s.startGdpr(w, r, true)
}

// gdprErase handles POST /v1/privacy/erase (manage_bookings): starts
// GdprEraseWorkflow which publishes a PrivacyEraseRequested tombstone
// CloudEvent to opendesk.privacy.events (consumed by booking/conversation/
// crm-sync to anonymize or delete the subject's data).
func (s *server) gdprErase(w http.ResponseWriter, r *http.Request) {
	s.startGdpr(w, r, false)
}

func (s *server) startGdpr(w http.ResponseWriter, r *http.Request, export bool) {
	tenant := tenantFrom(r.Context())
	if s.d.Gdpr == nil {
		writeError(w, http.StatusServiceUnavailable, "temporal unavailable; privacy workflows disabled")
		return
	}
	var req privacyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Phone == "" && req.Email == "" {
		writeError(w, http.StatusBadRequest, "phone or email is required")
		return
	}
	in := temporalclient.GdprRequest{
		TenantID:   tenant.ID.String(),
		TenantSlug: tenant.Slug,
		Phone:      req.Phone,
		Email:      req.Email,
	}
	var (
		workflowID string
		err        error
		kind       string
	)
	if export {
		kind = "export"
		workflowID, err = s.d.Gdpr.StartGdprExport(r.Context(), in)
	} else {
		kind = "erase"
		workflowID, err = s.d.Gdpr.StartGdprErase(r.Context(), in)
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":      "started",
		"kind":        kind,
		"workflow_id": workflowID,
	})
}
