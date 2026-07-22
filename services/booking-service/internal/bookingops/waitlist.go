package bookingops

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/store"
)

// ClaimInput describes a waitlist claim attempt (SPEC-W3 §3 innovation 7).
// The claim token is the capability secret delivered to the contact by the
// WaitlistBackfillWorkflow notification; StartsAt is the concrete slot the
// contact picked inside their window.
type ClaimInput struct {
	TenantID     uuid.UUID
	TenantSlug   string
	Timezone     string
	EntryID      uuid.UUID
	Token        uuid.UUID
	TeamMemberID uuid.UUID
	StartsAt     time.Time

	// SPEC-CRM §C3: same industry/policy plumbing as Create.
	Industry      string
	BookingPolicy *BookingPolicy
}

// ClaimWaitlist turns a waitlist entry into a real booking, transactionally
// re-validating token, window and slot availability in the store (see
// store.ClaimWaitlistTx). On success the booking saga starts and the
// availability cache is invalidated exactly like a regular create.
func (s *Service) ClaimWaitlist(ctx context.Context, in ClaimInput) (store.Booking, store.WaitlistEntry, error) {
	if in.EntryID == uuid.Nil || in.Token == uuid.Nil || in.TeamMemberID == uuid.Nil || in.StartsAt.IsZero() {
		return store.Booking{}, store.WaitlistEntry{}, fmt.Errorf("%w: entry, token, team member and starts_at are required", ErrInvalidInput)
	}

	entry, err := s.Store.GetWaitlistEntry(ctx, in.TenantID, in.EntryID)
	if err != nil {
		return store.Booking{}, store.WaitlistEntry{}, err
	}
	offering, err := s.Store.GetOffering(ctx, in.TenantID, entry.OfferingID)
	if err != nil {
		return store.Booking{}, store.WaitlistEntry{}, err
	}
	if _, err := s.Store.GetTeamMember(ctx, in.TenantID, in.TeamMemberID); err != nil {
		return store.Booking{}, store.WaitlistEntry{}, err
	}
	loc, err := loadLocation(in.Timezone)
	if err != nil {
		return store.Booking{}, store.WaitlistEntry{}, err
	}

	// Phone-confirmation policy: waitlist entries are only created with a
	// phone, but enforce it again here like Create does for contacts.
	if entry.ContactPhone == "" {
		return store.Booking{}, store.WaitlistEntry{}, ErrPhoneRequired
	}
	contact := store.Contact{
		TenantID: in.TenantID,
		Name:     entry.ContactName,
		Phone:    entry.ContactPhone,
	}
	if err := s.Store.CreateContact(ctx, &contact); err != nil {
		return store.Booking{}, store.WaitlistEntry{}, err
	}

	booking := store.Booking{
		TenantID:       in.TenantID,
		OfferingID:     entry.OfferingID,
		TeamMemberID:   in.TeamMemberID,
		ContactID:      contact.ID,
		StartsAt:       in.StartsAt,
		EndsAt:         in.StartsAt.Add(time.Duration(offering.DurationMin) * time.Minute),
		Status:         store.StatusPending,
		Source:         "web", // user-driven claim link (bookings.source CHECK)
		IdempotencyKey: "waitlist-claim:" + in.EntryID.String(),
	}
	payload, err := MarshalBookingEvent("com.opendesk.booking.BookingCreated", in.TenantSlug, booking, offering, contact)
	if err != nil {
		return store.Booking{}, store.WaitlistEntry{}, err
	}

	claimed, err := s.Store.ClaimWaitlistTx(ctx, in.TenantID, in.EntryID, in.Token, offering, &booking, loc, s.EventsTopic, payload)
	if err != nil {
		return store.Booking{}, store.WaitlistEntry{}, err
	}

	s.Cache.Invalidate(ctx, booking.TenantID, booking.OfferingID, booking.TeamMemberID, booking.StartsAt, booking.EndsAt)
	s.startSaga(ctx, booking, offering, contact, CreateInput{
		TenantSlug:    in.TenantSlug,
		Industry:      in.Industry,
		BookingPolicy: in.BookingPolicy,
	})
	return booking, claimed, nil
}
