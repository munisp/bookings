package httpapi

// Internal endpoints for the reverse CRM sync (Twenty -> OpenDesk,
// SPEC-CRM §B). They are invoked ONLY by crm-sync-service via Dapr service
// invocation, hence no Permify guard — tenant resolution is the usual
// X-Tenant-Slug middleware.
//
// LOOP PREVENTION: none of these handlers writes an outbox event. Contacts
// have no outbox at all (only booking lifecycle mutations do), and the
// crm-note append deliberately bypasses the outbox, so a reverse-synced
// change can never re-enter the forward OpenDesk -> Twenty event flow.

import (
	"errors"
	"net/http"
	"time"

	"github.com/opendesk/booking-service/internal/store"
)

// internalContactUpsertRequest is the payload of POST /internal/contacts/upsert.
type internalContactUpsertRequest struct {
	Name           string `json:"name"`
	Phone          string `json:"phone"`
	Email          string `json:"email"`
	Notes          string `json:"notes"`
	ExternalSource string `json:"external_source"` // e.g. "twenty"
	ExternalID     string `json:"external_id"`     // e.g. the Twenty person id
}

// upsertContactInternal (POST /internal/contacts/upsert) creates or updates a
// contact keyed by phone OR e-mail within the tenant. Contacts arriving with
// external_source set are stamped with source/external_id so later reverse
// events for the same CRM person re-match.
func (s *server) upsertContactInternal(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	var req internalContactUpsertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Phone == "" && req.Email == "" {
		writeError(w, http.StatusBadRequest, "phone or email is required")
		return
	}
	c := store.Contact{
		TenantID:   tenant.ID,
		Name:       req.Name,
		Phone:      req.Phone,
		Email:      req.Email,
		Notes:      req.Notes,
		Source:     req.ExternalSource,
		ExternalID: req.ExternalID,
	}
	created, err := s.d.Store.UpsertExternalContact(r.Context(), tenant.ID, &c)
	if err != nil {
		s.internal(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"contact": c, "created": created})
}

// lookupContactInternal (GET /internal/contacts?phone=|email=) is the lookup
// helper for the reverse sync worker: find a contact by phone OR e-mail.
func (s *server) lookupContactInternal(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	q := r.URL.Query()
	phone, email := q.Get("phone"), q.Get("email")
	if phone == "" && email == "" {
		writeError(w, http.StatusBadRequest, "phone or email query param is required")
		return
	}
	c, err := s.d.Store.FindContactByPhoneOrEmail(r.Context(), tenant.ID, phone, email)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "contact not found")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// addBookingCRMNoteInternal (POST /internal/bookings/{id}/crm-note) appends a
// CRM-originated note (e.g. "Twenty task ... marked DONE") to the booking's
// crm_notes JSONB array. Intentionally emits no outbox event.
func (s *server) addBookingCRMNoteInternal(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	id, ok := urlUUID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		Text   string `json:"text"`
		Source string `json:"source"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	if req.Source == "" {
		req.Source = "twenty"
	}
	note := store.CRMNote{At: time.Now().UTC(), Source: req.Source, Text: req.Text}
	if err := s.d.Store.AppendBookingCRMNote(r.Context(), tenant.ID, id, note); err != nil {
		s.mapOpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": note})
}
