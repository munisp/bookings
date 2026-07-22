package httpapi

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/bookingops"
	"github.com/opendesk/booking-service/internal/store"
)

// Waitlist backfill (SPEC-W3 §3 innovation 7). The claim endpoint is NOT
// behind manage_bookings on purpose: the claim_token delivered to the
// contact by the WaitlistBackfillWorkflow notification is the capability
// that authorizes the claim (a random UUID, unguessable, single-use via the
// transactional status flip).

type createWaitlistRequest struct {
	OfferingID   string    `json:"offering_id"`
	ContactName  string    `json:"contact_name"`
	ContactPhone string    `json:"contact_phone"`
	WindowStart  time.Time `json:"window_start"`
	WindowEnd    time.Time `json:"window_end"`
}

// createWaitlistEntry handles POST /v1/waitlist (manage_bookings).
func (s *server) createWaitlistEntry(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	var req createWaitlistRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	offeringID, err := uuid.Parse(req.OfferingID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "offering_id is required (uuid)")
		return
	}
	if req.ContactPhone == "" {
		writeError(w, http.StatusUnprocessableEntity, "contact_phone is required (phone-confirmation policy)")
		return
	}
	if req.WindowStart.IsZero() || req.WindowEnd.IsZero() || !req.WindowEnd.After(req.WindowStart) {
		writeError(w, http.StatusBadRequest, "window_start and window_end are required (window_end after window_start)")
		return
	}
	if _, err := s.d.Store.GetOffering(r.Context(), tenant.ID, offeringID); err != nil {
		s.mapOpError(w, err)
		return
	}
	entry := store.WaitlistEntry{
		TenantID:     tenant.ID,
		OfferingID:   offeringID,
		ContactName:  req.ContactName,
		ContactPhone: req.ContactPhone,
		WindowStart:  req.WindowStart,
		WindowEnd:    req.WindowEnd,
	}
	if err := s.d.Store.CreateWaitlistEntry(r.Context(), &entry); err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

// listWaitlist handles GET /v1/waitlist?offering_id&status= — also invoked
// by the notification-worker WaitlistBackfillWorkflow via Dapr service
// invocation (X-Tenant-Slug header, no manage_bookings required for reads).
func (s *server) listWaitlist(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	q := r.URL.Query()
	var f store.WaitlistFilter
	f.Status = q.Get("status")
	if v := q.Get("offering_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid offering_id")
			return
		}
		f.OfferingID = &id
	}
	items, err := s.d.Store.ListWaitlist(r.Context(), tenant.ID, f)
	if err != nil {
		s.internal(w, err)
		return
	}
	if items == nil {
		items = []store.WaitlistEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": items})
}

type claimWaitlistRequest struct {
	Token        string    `json:"token"`
	TeamMemberID string    `json:"team_member_id"`
	StartsAt     time.Time `json:"starts_at"`
}

// claimWaitlist handles POST /v1/waitlist/{id}/claim. Transactional in
// bookingops/store: token + window + slot re-check + booking insert +
// status flip happen atomically; any failure → 409 and nothing is written.
func (s *server) claimWaitlist(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	id, ok := urlUUID(w, r, "id")
	if !ok {
		return
	}
	var req claimWaitlistRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	token, err := uuid.Parse(req.Token)
	if err != nil {
		writeError(w, http.StatusBadRequest, "token is required (uuid)")
		return
	}
	teamMemberID, err := uuid.Parse(req.TeamMemberID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "team_member_id is required (uuid)")
		return
	}
	in := bookingops.ClaimInput{
		TenantID:     tenant.ID,
		TenantSlug:   tenant.Slug,
		Timezone:     tenant.Timezone,
		EntryID:      id,
		Token:        token,
		TeamMemberID: teamMemberID,
		StartsAt:     req.StartsAt,
		Industry:     tenant.Industry,
	}
	if tenant.Pack != nil {
		policy := tenant.Pack.BookingPolicy
		in.BookingPolicy = &policy
	}
	booking, entry, err := s.d.Ops.ClaimWaitlist(r.Context(), in)
	if err != nil {
		s.mapOpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"booking": booking,
		"entry":   entry,
	})
}
