package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/availability"
	"github.com/opendesk/booking-service/internal/cache"
	"github.com/opendesk/booking-service/internal/store"
	"go.uber.org/zap"
)

// getAvailability computes open slots:
// GET /v1/availability?offering_id&team_member_id&from&to
func (s *server) getAvailability(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	s.computeAvailability(w, r, tenant.ID, tenant.Location())
}

// computeAvailability is shared by the tenant-scoped and public endpoints.
func (s *server) computeAvailability(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, loc *time.Location) {
	q := r.URL.Query()
	offeringID, err := uuid.Parse(q.Get("offering_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "offering_id is required (uuid)")
		return
	}
	teamMemberID, err := uuid.Parse(q.Get("team_member_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "team_member_id is required (uuid)")
		return
	}
	from, err := parseQueryTime(q.Get("from"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "from must be RFC3339")
		return
	}
	to, err := parseQueryTime(q.Get("to"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "to must be RFC3339")
		return
	}
	if !to.After(from) {
		writeError(w, http.StatusBadRequest, "to must be after from")
		return
	}
	if to.Sub(from) > 62*24*time.Hour {
		writeError(w, http.StatusBadRequest, "range too large (max 62 days)")
		return
	}

	now := time.Now()
	if from.Before(now) {
		from = now
	}

	// Read-through availability cache (SPEC-W3 §3): one key per UTC day
	// bucket. A full hit answers without touching Postgres; any miss falls
	// through to the engine and re-populates every bucket of the range.
	if s.d.Cache.Enabled() {
		if slots, ok := s.cachedSlots(r, tenantID, offeringID, teamMemberID, from, to); ok {
			writeJSON(w, http.StatusOK, map[string]any{
				"offering_id":    offeringID,
				"team_member_id": teamMemberID,
				"slots":          slots,
			})
			return
		}
	}

	offering, err := s.d.Store.GetOffering(r.Context(), tenantID, offeringID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "offering not found")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	member, err := s.d.Store.GetTeamMember(r.Context(), tenantID, teamMemberID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "team member not found")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	if !member.Active {
		writeJSON(w, http.StatusOK, map[string]any{"slots": []availability.Slot{}})
		return
	}

	rules, err := s.d.Store.ListAvailabilityRules(r.Context(), tenantID, teamMemberID)
	if err != nil {
		s.internal(w, err)
		return
	}
	bookings, err := s.d.Store.ListBookingsForRange(r.Context(), tenantID, teamMemberID, from, to)
	if err != nil {
		s.internal(w, err)
		return
	}

	engineRules := make([]availability.Rule, 0, len(rules))
	for _, rl := range rules {
		engineRules = append(engineRules, availability.Rule{
			Weekday:       time.Weekday(rl.Weekday),
			StartMin:      rl.StartMin,
			EndMin:        rl.EndMin,
			EffectiveFrom: rl.EffectiveFrom,
			EffectiveTo:   rl.EffectiveTo,
		})
	}
	engineBookings := make([]availability.Booking, 0, len(bookings))
	for _, b := range bookings {
		engineBookings = append(engineBookings, availability.Booking{StartsAt: b.StartsAt, EndsAt: b.EndsAt})
	}

	slots := availability.Slots(availability.Params{
		From:     from,
		To:       to,
		Duration: time.Duration(offering.DurationMin) * time.Minute,
		Buffer:   time.Duration(offering.BufferMin) * time.Minute,
		Capacity: offering.Capacity,
		Rules:    engineRules,
		Bookings: engineBookings,
		Location: loc,
	})
	if slots == nil {
		slots = []availability.Slot{}
	}
	if s.d.Cache.Enabled() {
		s.populateCache(r, tenantID, offeringID, teamMemberID, from, to, slots)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"offering_id":    offeringID,
		"team_member_id": teamMemberID,
		"slots":          slots,
	})
}

// cachedSlots assembles the slot list from per-day cache entries. The
// second return value is false unless EVERY day bucket of [from, to] hits;
// partial ranges always fall back to the engine so no slice is ever served
// half-stale. Slots that have slid into the past since caching are dropped
// (the entry itself lives at most one TTL).
func (s *server) cachedSlots(r *http.Request, tenantID, offeringID, teamMemberID uuid.UUID, from, to time.Time) ([]availability.Slot, bool) {
	buckets := cache.DayBuckets(from, to)
	if len(buckets) == 0 {
		return nil, false
	}
	out := make([]availability.Slot, 0)
	for _, day := range buckets {
		slots, hit := s.d.Cache.GetSlots(r.Context(), cache.Key(tenantID, offeringID, teamMemberID, day))
		if !hit {
			return nil, false
		}
		for _, sl := range slots {
			// Clamp bucket edges back to the requested window.
			if sl.StartsAt.Before(from) || sl.EndsAt.After(to) {
				continue
			}
			out = append(out, sl)
		}
	}
	return out, true
}

// populateCache fans a computed slot list out into per-day bucket entries.
// Bucketing is by the slot's UTC start day, matching cache.DayBuckets.
func (s *server) populateCache(r *http.Request, tenantID, offeringID, teamMemberID uuid.UUID, from, to time.Time, slots []availability.Slot) {
	buckets := cache.DayBuckets(from, to)
	byDay := make(map[string][]availability.Slot, len(buckets))
	for _, sl := range slots {
		day := sl.StartsAt.UTC().Format("2006-01-02")
		byDay[day] = append(byDay[day], sl)
	}
	for _, day := range buckets {
		daySlots := byDay[day.Format("2006-01-02")]
		if daySlots == nil {
			daySlots = []availability.Slot{}
		}
		s.d.Cache.SetSlots(r.Context(), cache.Key(tenantID, offeringID, teamMemberID, day), daySlots)
	}
	s.d.Logger.Debug("availability cache populated",
		zap.String("tenant_id", tenantID.String()), zap.Int("buckets", len(buckets)))
}

func parseQueryTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, errEmpty
	}
	return time.Parse(time.RFC3339, v)
}

var errEmpty = errors.New("empty time value")

// parseOptionalDate accepts RFC3339 or YYYY-MM-DD, returning nil for "".
func parseOptionalDate(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return &t, nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return &t, nil
	}
	return nil, errEmpty
}
